package pywasi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ChrisRx/wazero"
	"github.com/ChrisRx/wazero/api"
	"github.com/ChrisRx/wazero/experimental/sock"
	experimentalsys "github.com/ChrisRx/wazero/experimental/sys"
	wazerosys "github.com/ChrisRx/wazero/sys"
)

// Sockets: WASI preview 1 can accept on, read from and write to sockets,
// but not create them. The interpreter's toolchain/support/pywasi_socket.c
// implements socket, bind, listen, connect, getaddrinfo and the rest with
// the host functions here, in the "pywasi" module. A socket is a
// placeholder descriptor until listen or connect, when it becomes a real
// listener or connection that wazero's own sock_accept, sock_recv,
// sock_send and poll_oneoff work on.

// network is the WithNetwork policy.
type network struct {
	enabled bool
	allow   func(op, address string) error
}

func (n network) check(ctx context.Context, op, address string) int32 {
	if !n.enabled {
		noteDisabled(ctx)
		return errnoAcces
	}
	if n.allow != nil && n.allow(op, address) != nil {
		return errnoAcces
	}
	return 0
}

// disabledKey marks a run's context with a *atomic.Bool, set when Python
// tried to use the network while WithNetwork is off. Eval and Call then add
// a hint to the PermissionError.
type disabledKey struct{}

func noteDisabled(ctx context.Context) {
	if b, ok := ctx.Value(disabledKey{}).(*atomic.Bool); ok {
		b.Store(true)
	}
}

// WASI errno values the host functions return.
const (
	errnoAcces        int32 = 2
	errnoAddrinuse    int32 = 3
	errnoAddrnotavail int32 = 4
	errnoAfnosupport  int32 = 5
	errnoAgain        int32 = 6
	errnoBadf         int32 = 8
	errnoConnaborted  int32 = 13
	errnoConnrefused  int32 = 14
	errnoConnreset    int32 = 15
	errnoDestaddrreq  int32 = 17
	errnoHostunreach  int32 = 23
	errnoIntr         int32 = 27
	errnoInval        int32 = 28
	errnoIO           int32 = 29
	errnoIsconn       int32 = 30
	errnoMsgsize      int32 = 35
	errnoNetdown      int32 = 38
	errnoNetunreach   int32 = 40
	errnoNoprotoopt   int32 = 50
	errnoNosys        int32 = 52
	errnoNotconn      int32 = 53
	errnoNotsock      int32 = 57
	errnoNotsup       int32 = 58
	errnoPerm         int32 = 63
	errnoProtonosupp  int32 = 66
	errnoTimedout     int32 = 73
)

// Results of sock_resolve and sock_lookup other than 0 and errno values.
const (
	resolveNoname int32 = 1000 + iota
	resolveAgain
	resolveFail
	resolveService
	resolveFamily
)

// sock_resolve flags.
const (
	resolveNumericHost = 1 << iota
	resolvePassive
	resolveNumericServ
)

// Socket options, numbered as in pywasi_socket.c.
const (
	optReuseAddr int32 = 1 + iota
	optKeepAlive
	optNoDelay
	optRcvBuf
	optSndBuf
	optError
	optType
	optDomain
	optAcceptConn
	optReusePort
	optBroadcast
	optV6Only
)

// wireAddrSize is the size of an address in guest memory: family (4 or 6)
// and port as uint16, flowinfo and scope ID as uint32, then 16 bytes of
// address.
const wireAddrSize = 28

// instantiateHost adds the "pywasi" host module to r.
func instantiateHost(ctx context.Context, r wazero.Runtime, n network) error {
	h := sockets{net: n}
	b := r.NewHostModuleBuilder("pywasi")
	for _, f := range []struct {
		name   string
		fn     api.GoModuleFunc
		params int
	}{
		{"sock_open", h.open, 4},
		{"sock_bind", h.bind, 2},
		{"sock_listen", h.listen, 2},
		{"sock_connect", h.connect, 2},
		{"sock_addr", h.addr, 3},
		{"sock_getopt", h.getopt, 3},
		{"sock_setopt", h.setopt, 3},
		{"sock_resolve", h.resolve, 9},
		{"sock_lookup", h.lookup, 4},
		{"sock_pair", h.pair, 3},
		{"sock_sendto", h.sendto, 6},
		{"sock_recvfrom", h.recvfrom, 6},
	} {
		params := make([]api.ValueType, f.params)
		for i := range params {
			params[i] = api.ValueTypeI32
		}
		b.NewFunctionBuilder().WithGoModuleFunction(f.fn, params, []api.ValueType{api.ValueTypeI32}).Export(f.name)
	}
	_, err := b.Instantiate(ctx)
	return err
}

type sockets struct {
	net network
}

// pendingSocket is a socket that isn't listening or connected yet.
type pendingSocket struct {
	experimentalsys.UnimplementedFile
	family   int32 // 4 or 6
	dgram    bool  // UDP, else TCP
	nonblock bool
	// listener is bound by bind, and becomes the socket on listen.
	listener *net.TCPListener
	// opts are options set so far, applied on bind or connect.
	opts map[int32]int32
}

func (s *pendingSocket) IsDir() (bool, experimentalsys.Errno) { return false, 0 }

func (s *pendingSocket) Stat() (st wazerosys.Stat_t, errno experimentalsys.Errno) {
	st.Mode = fs.ModeSocket
	return st, 0
}

func (s *pendingSocket) IsNonblock() bool { return s.nonblock }

func (s *pendingSocket) SetNonblock(enable bool) experimentalsys.Errno {
	s.nonblock = enable
	return 0
}

func (s *pendingSocket) Poll(experimentalsys.Pflag, int32) (bool, experimentalsys.Errno) {
	return false, experimentalsys.ENOTSUP
}

func (s *pendingSocket) Close() experimentalsys.Errno {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	return 0
}

func (s *pendingSocket) network() string {
	proto := "tcp"
	if s.dgram {
		proto = "udp"
	}
	if s.family == 6 {
		return proto + "6"
	}
	return proto + "4"
}

// listenUDP binds a UDP socket to addr. A wildcard IPv6 socket receives
// IPv4 too, as on Linux, unless IPV6_V6ONLY is set.
func (s *pendingSocket) listenUDP(addr netip.AddrPort) (*net.UDPConn, error) {
	network := s.network()
	if s.family == 6 && addr.Addr().IsUnspecified() && s.opts[optV6Only] == 0 {
		network = "udp"
	}
	lc := net.ListenConfig{Control: s.control}
	c, err := lc.ListenPacket(context.Background(), network, addr.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}

// wildcard is the address for binding s to any port.
func (s *pendingSocket) wildcard() netip.AddrPort {
	if s.family == 6 {
		return netip.AddrPortFrom(netip.IPv6Unspecified(), 0)
	}
	return netip.AddrPortFrom(netip.IPv4Unspecified(), 0)
}

// control applies the options set so far to a new host socket.
func (s *pendingSocket) control(_, _ string, c syscall.RawConn) error {
	var err error
	if cerr := c.Control(func(fd uintptr) {
		for opt, v := range s.opts {
			if opt == optV6Only {
				continue // Decided by the network passed to Listen.
			}
			if errno := setSockopt(fd, opt, v); errno != 0 && errno != errnoNoprotoopt {
				err = fmt.Errorf("setting socket option %d: errno %d", opt, errno)
				return
			}
		}
	}); cerr != nil {
		return cerr
	}
	return err
}

func (h sockets) open(ctx context.Context, mod api.Module, stack []uint64) {
	family, kind, nonblock, out := int32(stack[0]), int32(stack[1]), uint32(stack[2]) != 0, uint32(stack[3])
	stack[0] = uint64(uint32(func() int32 {
		if family != 4 && family != 6 {
			return errnoAfnosupport
		}
		if kind != 1 && kind != 2 { // TCP or UDP
			return errnoProtonosupp
		}
		if !h.net.enabled {
			noteDisabled(ctx)
			return errnoAcces
		}
		fd, err := sock.Open(mod, &pendingSocket{family: family, dgram: kind == 2, nonblock: nonblock, opts: map[int32]int32{}})
		if err != nil {
			return errnoIO
		}
		if !mod.Memory().WriteUint32Le(out, uint32(fd)) {
			return errnoInval
		}
		return 0
	}()))
}

// pending returns the socket fd if it isn't listening or connected yet,
// else an errno for the call, which wants a pending socket.
func pending(mod api.Module, fd int32) (*pendingSocket, int32) {
	f, ok := sock.Lookup(mod, fd)
	if !ok {
		return nil, errnoBadf
	}
	switch f := f.(type) {
	case *pendingSocket:
		return f, 0
	case *net.TCPListener:
		return nil, errnoInval
	case *net.TCPConn:
		return nil, errnoIsconn
	case *net.UDPConn:
		return nil, errnoInval // Bound.
	}
	return nil, errnoNotsock
}

func (h sockets) bind(ctx context.Context, mod api.Module, stack []uint64) {
	fd, addrPtr := int32(stack[0]), uint32(stack[1])
	stack[0] = uint64(uint32(func() int32 {
		s, errno := pending(mod, fd)
		if errno != 0 {
			return errno
		}
		if s.listener != nil {
			return errnoInval
		}
		addr, ok := readAddr(mod, addrPtr)
		if !ok {
			return errnoInval
		}
		if addr.Addr().Is4() != (s.family == 4) {
			return errnoAfnosupport
		}
		if s.dgram {
			// A bound UDP socket is ready to send and receive.
			if errno := h.net.check(ctx, "listen", addr.String()); errno != 0 {
				return errno
			}
			c, err := s.listenUDP(addr)
			if err != nil {
				return errnoOf(err)
			}
			return attach(mod, fd, c)
		}
		return h.bindTo(ctx, s, addr)
	}()))
}

func (h sockets) bindTo(ctx context.Context, s *pendingSocket, addr netip.AddrPort) int32 {
	if errno := h.net.check(ctx, "listen", addr.String()); errno != 0 {
		return errnoAcces
	}
	// A wildcard IPv6 socket accepts IPv4 too, as on Linux, unless
	// IPV6_V6ONLY is set.
	network := s.network()
	if s.family == 6 && addr.Addr().IsUnspecified() && s.opts[optV6Only] == 0 {
		network = "tcp"
	}
	lc := net.ListenConfig{Control: s.control}
	ln, err := lc.Listen(context.Background(), network, addr.String())
	if err != nil {
		return errnoOf(err)
	}
	s.listener = ln.(*net.TCPListener)
	return 0
}

func (h sockets) listen(ctx context.Context, mod api.Module, stack []uint64) {
	fd := int32(stack[0])
	stack[0] = uint64(uint32(func() int32 {
		if f, _ := sock.Lookup(mod, fd); isUDP(f) {
			return errnoNotsup
		}
		s, errno := pending(mod, fd)
		if errno == errnoInval {
			return 0 // Already listening.
		} else if errno != 0 {
			return errno
		}
		if s.listener == nil {
			// Bind to an ephemeral port first, as POSIX does.
			any := netip.IPv4Unspecified()
			if s.family == 6 {
				any = netip.IPv6Unspecified()
			}
			if errno := h.bindTo(ctx, s, netip.AddrPortFrom(any, 0)); errno != 0 {
				return errno
			}
		}
		if err := sock.Attach(mod, fd, s.listener); err != nil {
			return errnoIO
		}
		s.listener = nil // Owned by the descriptor now.
		return 0
	}()))
}

func (h sockets) connect(ctx context.Context, mod api.Module, stack []uint64) {
	fd, addrPtr := int32(stack[0]), uint32(stack[1])
	stack[0] = uint64(uint32(func() int32 {
		if f, _ := sock.Lookup(mod, fd); isUDP(f) {
			return h.connectUDP(ctx, mod, fd, f, addrPtr)
		}
		s, errno := pending(mod, fd)
		if errno == errnoInval {
			return errnoIsconn // A listener.
		} else if errno != 0 {
			return errno
		}
		addr, ok := readAddr(mod, addrPtr)
		if !ok {
			return errnoInval
		}
		if errno := h.net.check(ctx, "connect", addr.String()); errno != 0 {
			return errno
		}
		d := net.Dialer{Control: s.control}
		if s.listener != nil {
			// Bound first: connect from that address.
			d.LocalAddr = s.listener.Addr()
			_ = s.listener.Close()
			s.listener = nil
		}
		conn, err := d.DialContext(ctx, s.network(), addr.String())
		if err != nil {
			exitIfDone(ctx, mod)
			return errnoOf(err)
		}
		if err := sock.Attach(mod, fd, conn); err != nil {
			_ = conn.Close()
			return errnoIO
		}
		return 0
	}()))
}

// connectUDP sets the destination of a UDP socket, binding it first if
// needed.
func (h sockets) connectUDP(ctx context.Context, mod api.Module, fd int32, f any, addrPtr uint32) int32 {
	addr, ok := readAddr(mod, addrPtr)
	if !ok {
		return errnoInval
	}
	if errno := h.net.check(ctx, "connect", addr.String()); errno != 0 {
		return errno
	}
	if c, ok := f.(*net.UDPConn); ok {
		return connectRaw(c, addr)
	}
	s := f.(*pendingSocket)
	d := net.Dialer{Control: s.control}
	c, err := d.DialContext(ctx, s.network(), addr.String())
	if err != nil {
		return errnoOf(err)
	}
	return attach(mod, fd, c)
}

// udpConn is the host socket of a UDP socket, which is bound to any port
// first if it isn't yet, as POSIX does on sending.
func udpConn(mod api.Module, fd int32) (*net.UDPConn, int32) {
	f, ok := sock.Lookup(mod, fd)
	if !ok {
		return nil, errnoBadf
	}
	switch f := f.(type) {
	case *net.UDPConn:
		return f, 0
	case *pendingSocket:
		if !f.dgram {
			return nil, errnoNotconn
		}
		c, err := f.listenUDP(f.wildcard())
		if err != nil {
			return nil, errnoOf(err)
		}
		if errno := attach(mod, fd, c); errno != 0 {
			return nil, errno
		}
		return c, 0
	case *net.TCPConn, *net.TCPListener:
		return nil, errnoIsconn
	}
	return nil, errnoNotsock
}

// Flags of sock_sendto and sock_recvfrom.
const (
	dgramPeek     = 1
	dgramDontWait = 2
)

// sendto sends a datagram to an address, which the policy checks as
// "connect".
func (h sockets) sendto(ctx context.Context, mod api.Module, stack []uint64) {
	fd, buf, n, flags := int32(stack[0]), uint32(stack[1]), uint32(stack[2]), int32(stack[3])
	addrPtr, sentPtr := uint32(stack[4]), uint32(stack[5])
	stack[0] = uint64(uint32(func() int32 {
		addr, ok := readAddr(mod, addrPtr)
		if !ok {
			return errnoInval
		}
		data, ok := mod.Memory().Read(buf, n)
		if !ok {
			return errnoInval
		}
		if errno := h.net.check(ctx, "connect", addr.String()); errno != 0 {
			return errno
		}
		c, errno := udpConn(mod, fd)
		if errno != 0 {
			return errno
		}
		sent, errno := dgramSend(ctx, mod, c, data, addr, flags)
		if errno != 0 {
			return errno
		}
		if !mod.Memory().WriteUint32Le(sentPtr, uint32(sent)) {
			return errnoInval
		}
		return 0
	}()))
}

// recvfrom receives a datagram and the address it came from, or data and
// the peer address on a TCP connection.
func (h sockets) recvfrom(ctx context.Context, mod api.Module, stack []uint64) {
	fd, buf, n, flags := int32(stack[0]), uint32(stack[1]), uint32(stack[2]), int32(stack[3])
	addrPtr, nPtr := uint32(stack[4]), uint32(stack[5])
	stack[0] = uint64(uint32(func() int32 {
		data, ok := mod.Memory().Read(buf, n)
		if !ok {
			return errnoInval
		}
		var c syscall.Conn
		if f, ok := sock.Lookup(mod, fd); !ok {
			return errnoBadf
		} else if tc, ok := f.(*net.TCPConn); ok {
			c = tc
		} else {
			uc, errno := udpConn(mod, fd)
			if errno != 0 {
				return errno
			}
			c = uc
		}
		got, from, errno := dgramRecv(ctx, mod, c, data, flags)
		if errno != 0 {
			return errno
		}
		if !writeAddr(mod, addrPtr, from) || !mod.Memory().WriteUint32Le(nPtr, uint32(got)) {
			return errnoInval
		}
		return 0
	}()))
}

func isUDP(f any) bool {
	switch f := f.(type) {
	case *net.UDPConn:
		return true
	case *pendingSocket:
		return f.dgram
	}
	return false
}

func attach(mod api.Module, fd int32, conn interface{ Close() error }) int32 {
	if err := sock.Attach(mod, fd, conn); err != nil {
		_ = conn.Close()
		return errnoIO
	}
	return 0
}

// pair makes a connected pair of sockets for socketpair, from loopback
// TCP. It is internal to the process, so it needs no network access.
func (h sockets) pair(_ context.Context, mod api.Module, stack []uint64) {
	family, nonblock, out := int32(stack[0]), uint32(stack[1]) != 0, uint32(stack[2])
	stack[0] = uint64(uint32(func() int32 {
		a, b, err := loopbackPair(family)
		if err != nil {
			return errnoOf(err)
		}
		var fds [2]int32
		for i, c := range []*net.TCPConn{a, b} {
			fd, err := sock.Open(mod, &pendingSocket{family: family, nonblock: nonblock})
			if err == nil {
				err = sock.Attach(mod, fd, c)
			}
			if err != nil {
				_ = a.Close()
				_ = b.Close()
				return errnoIO
			}
			fds[i] = fd
		}
		if !mod.Memory().WriteUint32Le(out, uint32(fds[0])) || !mod.Memory().WriteUint32Le(out+4, uint32(fds[1])) {
			return errnoInval
		}
		return 0
	}()))
}

// loopbackPair connects two TCP sockets over loopback. The listener lives
// only until its first connection from the dialing socket, and anything
// else that connects meanwhile is dropped.
func loopbackPair(family int32) (*net.TCPConn, *net.TCPConn, error) {
	network, ip := "tcp4", net.IPv4(127, 0, 0, 1)
	if family == 6 {
		network, ip = "tcp6", net.IPv6loopback
	}
	ln, err := net.ListenTCP(network, &net.TCPAddr{IP: ip})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = ln.Close() }()
	if err := ln.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, nil, err
	}
	a, err := net.DialTCP(network, nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		return nil, nil, err
	}
	for {
		b, err := ln.AcceptTCP()
		if err != nil {
			_ = a.Close()
			return nil, nil, err
		}
		if b.RemoteAddr().String() == a.LocalAddr().String() {
			return a, b, nil
		}
		_ = b.Close()
	}
}

func (h sockets) addr(_ context.Context, mod api.Module, stack []uint64) {
	fd, peer, out := int32(stack[0]), uint32(stack[1]) != 0, uint32(stack[2])
	stack[0] = uint64(uint32(func() int32 {
		f, ok := sock.Lookup(mod, fd)
		if !ok {
			return errnoBadf
		}
		var a net.Addr
		switch f := f.(type) {
		case *pendingSocket:
			if peer {
				return errnoNotconn
			}
			if f.listener != nil {
				a = f.listener.Addr()
			} else if f.family == 6 {
				a = &net.TCPAddr{IP: net.IPv6unspecified}
			} else {
				a = &net.TCPAddr{IP: net.IPv4zero}
			}
		case *net.TCPListener:
			if peer {
				return errnoNotconn
			}
			a = f.Addr()
		case *net.TCPConn:
			if peer {
				a = f.RemoteAddr()
			} else {
				a = f.LocalAddr()
			}
		case *net.UDPConn:
			if peer {
				// Connected after binding, which Go doesn't know of.
				ap, errno := peerAddr(f)
				if errno != 0 {
					return errno
				}
				a = net.TCPAddrFromAddrPort(ap)
			} else {
				a = net.TCPAddrFromAddrPort(f.LocalAddr().(*net.UDPAddr).AddrPort())
			}
		default:
			return errnoNotsock
		}
		ap := a.(*net.TCPAddr).AddrPort()
		if !writeAddr(mod, out, ap) {
			return errnoInval
		}
		return 0
	}()))
}

func (h sockets) getopt(_ context.Context, mod api.Module, stack []uint64) {
	fd, opt, out := int32(stack[0]), int32(stack[1]), uint32(stack[2])
	stack[0] = uint64(uint32(func() int32 {
		f, ok := sock.Lookup(mod, fd)
		if !ok {
			return errnoBadf
		}
		var v int32
		switch opt {
		case optError:
			v = 0 // connect is synchronous.
		case optType:
			v = 1
			if isUDP(f) {
				v = 2
			}
		case optDomain:
			v = 4
			if a, ok := sockAddr(f); ok && !a.Addr().Unmap().Is4() {
				v = 6
			} else if s, ok := f.(*pendingSocket); ok {
				v = s.family
			}
		case optAcceptConn:
			if _, ok := f.(*net.TCPListener); ok {
				v = 1
			}
		default:
			switch f := f.(type) {
			case *pendingSocket:
				v = f.opts[opt]
			case syscall.Conn:
				var errno int32
				if v, errno = rawGetSockopt(f, opt); errno != 0 {
					return errno
				}
			default:
				return errnoNotsock
			}
		}
		if !mod.Memory().WriteUint32Le(out, uint32(v)) {
			return errnoInval
		}
		return 0
	}()))
}

func (h sockets) setopt(_ context.Context, mod api.Module, stack []uint64) {
	fd, opt, value := int32(stack[0]), int32(stack[1]), int32(stack[2])
	stack[0] = uint64(uint32(func() int32 {
		f, ok := sock.Lookup(mod, fd)
		if !ok {
			return errnoBadf
		}
		switch opt {
		case optError, optType, optDomain, optAcceptConn:
			return errnoNoprotoopt
		}
		switch f := f.(type) {
		case *pendingSocket:
			if f.listener != nil && opt == optV6Only {
				return errnoInval // Too late, as on Linux.
			}
			f.opts[opt] = value
			if f.listener != nil {
				return rawSetSockopt(f.listener, opt, value)
			}
			return 0
		case syscall.Conn:
			return rawSetSockopt(f, opt, value)
		}
		return errnoNotsock
	}()))
}

func sockAddr(f any) (netip.AddrPort, bool) {
	switch f := f.(type) {
	case *net.TCPListener:
		return f.Addr().(*net.TCPAddr).AddrPort(), true
	case *net.TCPConn:
		return f.LocalAddr().(*net.TCPAddr).AddrPort(), true
	case *net.UDPConn:
		return f.LocalAddr().(*net.UDPAddr).AddrPort(), true
	}
	return netip.AddrPort{}, false
}

func rawGetSockopt(c syscall.Conn, opt int32) (v int32, errno int32) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, errnoOf(err)
	}
	if err := raw.Control(func(fd uintptr) { v, errno = getSockopt(fd, opt) }); err != nil {
		return 0, errnoOf(err)
	}
	return v, errno
}

func rawSetSockopt(c syscall.Conn, opt, v int32) (errno int32) {
	raw, err := c.SyscallConn()
	if err != nil {
		return errnoOf(err)
	}
	if err := raw.Control(func(fd uintptr) { errno = setSockopt(fd, opt, v) }); err != nil {
		return errnoOf(err)
	}
	return errno
}

func (h sockets) resolve(ctx context.Context, mod api.Module, stack []uint64) {
	hostPtr, hostLen := uint32(stack[0]), uint32(stack[1])
	svcPtr, svcLen := uint32(stack[2]), uint32(stack[3])
	family, flags := int32(stack[4]), int32(stack[5])
	out, capacity, nOut := uint32(stack[6]), uint32(stack[7]), uint32(stack[8])
	stack[0] = uint64(uint32(func() int32 {
		host, ok := readString(mod, hostPtr, hostLen)
		if !ok {
			return errnoInval
		}
		service, ok := readString(mod, svcPtr, svcLen)
		if !ok {
			return errnoInval
		}
		port, errno := h.lookupPort(ctx, service, flags&resolveNumericServ != 0)
		if errno != 0 {
			return errno
		}
		addrs, errno := h.lookupHost(ctx, host, family, flags)
		if errno == errnoIntr {
			exitIfDone(ctx, mod)
		}
		if errno != 0 {
			return errno
		}
		n := uint32(0)
		for _, a := range addrs {
			if n == capacity {
				break
			}
			if !writeAddr(mod, out+n*wireAddrSize, netip.AddrPortFrom(a, port)) {
				return errnoInval
			}
			n++
		}
		if !mod.Memory().WriteUint32Le(nOut, n) {
			return errnoInval
		}
		return 0
	}()))
}

func (h sockets) lookupPort(ctx context.Context, service string, numeric bool) (uint16, int32) {
	if service == "" {
		return 0, 0
	}
	if p, err := strconv.ParseUint(service, 10, 16); err == nil {
		return uint16(p), 0
	}
	if numeric {
		return 0, resolveService
	}
	if errno := h.net.check(ctx, "resolve", service); errno != 0 {
		return 0, errno
	}
	p, err := net.DefaultResolver.LookupPort(ctx, "tcp", service)
	if err != nil {
		return 0, resolveService
	}
	return uint16(p), 0
}

// lookupHost resolves host for getaddrinfo: IPv4 addresses first, as glibc
// orders them for wildcard and loopback names.
func (h sockets) lookupHost(ctx context.Context, host string, family, flags int32) ([]netip.Addr, int32) {
	keep := func(addrs ...netip.Addr) ([]netip.Addr, int32) {
		var out []netip.Addr
		for _, a := range addrs {
			if family == 0 || (family == 4) == a.Unmap().Is4() {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			return nil, resolveNoname
		}
		return out, 0
	}
	switch {
	case host == "" && flags&resolvePassive != 0:
		return keep(netip.IPv4Unspecified(), netip.IPv6Unspecified())
	case host == "", strings.EqualFold(host, "localhost"), strings.EqualFold(host, "localhost."):
		return keep(netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback())
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if family == 6 && a.Is4() {
			return nil, resolveFamily
		}
		return keep(a)
	}
	if flags&resolveNumericHost != 0 {
		return nil, resolveNoname
	}
	// An errno makes getaddrinfo fail with EAI_SYSTEM, which Python
	// raises as an OSError (PermissionError) instead of a gaierror.
	if errno := h.net.check(ctx, "resolve", host); errno != 0 {
		return nil, errno
	}
	network := "ip"
	switch family {
	case 4:
		network = "ip4"
	case 6:
		network = "ip6"
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errnoIntr
		}
		return nil, resolveErrno(err)
	}
	// IPv4 first, keeping the resolver's order otherwise.
	sorted := make([]netip.Addr, 0, len(addrs))
	for _, v4 := range []bool{true, false} {
		for _, a := range addrs {
			if a.Unmap().Is4() == v4 {
				sorted = append(sorted, a.Unmap())
			}
		}
	}
	return keep(sorted...)
}

func (h sockets) lookup(ctx context.Context, mod api.Module, stack []uint64) {
	addrPtr, out, capacity, lenOut := uint32(stack[0]), uint32(stack[1]), uint32(stack[2]), uint32(stack[3])
	stack[0] = uint64(uint32(func() int32 {
		ap, ok := readAddr(mod, addrPtr)
		if !ok {
			return errnoInval
		}
		a := ap.Addr().Unmap()
		var name string
		switch {
		case a.IsLoopback():
			name = "localhost"
		case a.IsUnspecified() || !h.net.enabled:
			return resolveNoname
		default:
			if errno := h.net.check(ctx, "resolve", a.String()); errno != 0 {
				return errno
			}
			names, err := net.DefaultResolver.LookupAddr(ctx, a.String())
			if err != nil {
				return resolveErrno(err)
			}
			if len(names) == 0 {
				return resolveNoname
			}
			name = strings.TrimSuffix(names[0], ".")
		}
		if uint32(len(name)) > capacity {
			name = name[:capacity]
		}
		if !mod.Memory().Write(out, []byte(name)) || !mod.Memory().WriteUint32Le(lenOut, uint32(len(name))) {
			return errnoInval
		}
		return 0
	}()))
}

// exitIfDone exits the module when ctx is done, as the fork's WASI
// functions do while blocked, so Run returns ctx.Err().
func exitIfDone(ctx context.Context, mod api.Module) {
	err := ctx.Err()
	if err == nil {
		return
	}
	code := wazerosys.ExitCodeContextCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		code = wazerosys.ExitCodeDeadlineExceeded
	}
	_ = mod.CloseWithExitCode(ctx, code)
	panic(wazerosys.NewExitError(code))
}

func resolveErrno(err error) int32 {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return resolveNoname
		case dnsErr.IsTemporary, dnsErr.IsTimeout:
			return resolveAgain
		}
	}
	return resolveFail
}

func readString(mod api.Module, ptr, n uint32) (string, bool) {
	if n == 0 {
		return "", true
	}
	b, ok := mod.Memory().Read(ptr, n)
	return string(b), ok
}

func readAddr(mod api.Module, ptr uint32) (netip.AddrPort, bool) {
	b, ok := mod.Memory().Read(ptr, wireAddrSize)
	if !ok {
		return netip.AddrPort{}, false
	}
	port := binary.LittleEndian.Uint16(b[2:])
	switch binary.LittleEndian.Uint16(b) {
	case 4:
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[12:16])), port), true
	case 6:
		a := netip.AddrFrom16([16]byte(b[12:28]))
		if scope := binary.LittleEndian.Uint32(b[8:]); scope != 0 {
			a = a.WithZone(strconv.FormatUint(uint64(scope), 10))
		}
		return netip.AddrPortFrom(a, port), true
	}
	return netip.AddrPort{}, false
}

func writeAddr(mod api.Module, ptr uint32, ap netip.AddrPort) bool {
	var b [wireAddrSize]byte
	a := ap.Addr()
	binary.LittleEndian.PutUint16(b[2:], ap.Port())
	if a.Is4() {
		binary.LittleEndian.PutUint16(b[0:], 4)
		v4 := a.As4()
		copy(b[12:], v4[:])
	} else {
		binary.LittleEndian.PutUint16(b[0:], 6)
		v6 := a.As16()
		copy(b[12:], v6[:])
		if z, err := strconv.ParseUint(a.Zone(), 10, 32); err == nil {
			binary.LittleEndian.PutUint32(b[8:], uint32(z))
		}
	}
	return mod.Memory().Write(ptr, b[:])
}

// errnoOf maps a host networking error to a WASI errno.
func errnoOf(err error) int32 {
	for _, e := range []struct {
		err   error
		errno int32
	}{
		{syscall.EACCES, errnoAcces},
		{syscall.EPERM, errnoPerm},
		{syscall.EADDRINUSE, errnoAddrinuse},
		{syscall.EADDRNOTAVAIL, errnoAddrnotavail},
		{syscall.EAFNOSUPPORT, errnoAfnosupport},
		{syscall.ECONNABORTED, errnoConnaborted},
		{syscall.ECONNREFUSED, errnoConnrefused},
		{syscall.ECONNRESET, errnoConnreset},
		{syscall.EHOSTUNREACH, errnoHostunreach},
		{syscall.ENETDOWN, errnoNetdown},
		{syscall.ENETUNREACH, errnoNetunreach},
		{syscall.ETIMEDOUT, errnoTimedout},
		{syscall.EINVAL, errnoInval},
		{os.ErrDeadlineExceeded, errnoTimedout},
		{context.DeadlineExceeded, errnoTimedout},
		{context.Canceled, errnoIntr},
	} {
		if errors.Is(err, e.err) {
			return e.errno
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errnoHostunreach
	}
	return errnoIO
}
