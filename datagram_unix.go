//go:build linux || darwin

package pywasi

import (
	"context"
	"net/netip"
	"strconv"
	"syscall"

	"github.com/ChrisRx/wazero/api"
	"golang.org/x/sys/unix"
)

// Datagram calls on host sockets, for sock_sendto and sock_recvfrom. They
// never block in the kernel: when the socket isn't ready and the guest
// wants to wait, they poll in short steps so that a done context stops
// them, as the fork's WASI calls do.

func dgramSend(ctx context.Context, mod api.Module, c syscall.Conn, data []byte, to netip.AddrPort, flags int32) (int, int32) {
	for {
		var errno int32
		err := control(c, func(fd int) {
			sa, ok := toSockaddr(fd, to)
			if !ok {
				errno = errnoAfnosupport
				return
			}
			errno = errnoOfSys(unix.Sendto(fd, data, unix.MSG_DONTWAIT, sa))
		})
		if err != 0 {
			return 0, err
		}
		if errno != errnoAgain || flags&dgramDontWait != 0 {
			if errno != 0 {
				return 0, errno
			}
			return len(data), 0
		}
		if errno := waitFd(ctx, mod, c, unix.POLLOUT); errno != 0 {
			return 0, errno
		}
	}
}

func dgramRecv(ctx context.Context, mod api.Module, c syscall.Conn, buf []byte, flags int32) (int, netip.AddrPort, int32) {
	how := unix.MSG_DONTWAIT
	if flags&dgramPeek != 0 {
		how |= unix.MSG_PEEK
	}
	for {
		var (
			n     int
			from  netip.AddrPort
			errno int32
		)
		err := control(c, func(fd int) {
			var sa unix.Sockaddr
			var e error
			n, sa, e = unix.Recvfrom(fd, buf, how)
			if errno = errnoOfSys(e); errno != 0 {
				return
			}
			if sa == nil { // A connected stream.
				if sa, e = unix.Getpeername(fd); e != nil {
					errno = errnoOfSys(e)
					return
				}
			}
			from = fromSockaddr(sa)
		})
		if err != 0 {
			return 0, from, err
		}
		if errno != errnoAgain || flags&dgramDontWait != 0 {
			return n, from, errno
		}
		if errno := waitFd(ctx, mod, c, unix.POLLIN); errno != 0 {
			return 0, from, errno
		}
	}
}

// connectRaw connects a bound UDP socket to a default destination.
func connectRaw(c syscall.Conn, to netip.AddrPort) int32 {
	var errno int32
	if err := control(c, func(fd int) {
		sa, ok := toSockaddr(fd, to)
		if !ok {
			errno = errnoAfnosupport
			return
		}
		errno = errnoOfSys(unix.Connect(fd, sa))
	}); err != 0 {
		return err
	}
	return errno
}

// peerAddr is the address a socket is connected to.
func peerAddr(c syscall.Conn) (netip.AddrPort, int32) {
	var (
		ap    netip.AddrPort
		errno int32
	)
	if err := control(c, func(fd int) {
		sa, e := unix.Getpeername(fd)
		if errno = errnoOfSys(e); errno == 0 {
			ap = fromSockaddr(sa)
		}
	}); err != 0 {
		return ap, err
	}
	return ap, errno
}

// waitFd waits until c is ready for events, or the context is done.
func waitFd(ctx context.Context, mod api.Module, c syscall.Conn, events int16) int32 {
	for {
		var ready bool
		var errno int32
		if err := control(c, func(fd int) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
			n, e := unix.Poll(fds, 100)
			if e == unix.EINTR {
				return
			}
			errno = errnoOfSys(e)
			ready = n > 0
		}); err != 0 {
			return err
		}
		if errno != 0 || ready {
			return errno
		}
		exitIfDone(ctx, mod)
	}
}

func control(c syscall.Conn, fn func(fd int)) int32 {
	raw, err := c.SyscallConn()
	if err != nil {
		return errnoOf(err)
	}
	if err := raw.Control(func(fd uintptr) { fn(int(fd)) }); err != nil {
		return errnoOf(err)
	}
	return 0
}

// toSockaddr is to as an address for the socket fd: IPv4 addresses are
// mapped for an IPv6 socket.
func toSockaddr(fd int, to netip.AddrPort) (unix.Sockaddr, bool) {
	local, err := unix.Getsockname(fd)
	if err != nil {
		return nil, false
	}
	a := to.Addr()
	if _, v6 := local.(*unix.SockaddrInet6); v6 {
		sa := &unix.SockaddrInet6{Port: int(to.Port()), Addr: a.As16()}
		if z, err := strconv.ParseUint(a.Zone(), 10, 32); err == nil {
			sa.ZoneId = uint32(z)
		}
		return sa, true
	}
	if !a.Unmap().Is4() {
		return nil, false
	}
	return &unix.SockaddrInet4{Port: int(to.Port()), Addr: a.Unmap().As4()}, true
}

func fromSockaddr(sa unix.Sockaddr) netip.AddrPort {
	switch sa := sa.(type) {
	case *unix.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port))
	case *unix.SockaddrInet6:
		a := netip.AddrFrom16(sa.Addr)
		if sa.ZoneId != 0 {
			a = a.WithZone(strconv.FormatUint(uint64(sa.ZoneId), 10))
		}
		return netip.AddrPortFrom(a, uint16(sa.Port))
	}
	return netip.AddrPort{}
}

func errnoOfSys(err error) int32 {
	switch err {
	case nil:
		return 0
	case unix.EAGAIN:
		return errnoAgain
	case unix.EMSGSIZE:
		return errnoMsgsize
	case unix.EISCONN:
		return errnoIsconn
	case unix.ENOTCONN:
		return errnoNotconn
	case unix.EDESTADDRREQ:
		return errnoDestaddrreq
	}
	return errnoOf(err)
}
