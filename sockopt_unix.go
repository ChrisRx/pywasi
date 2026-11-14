//go:build linux || darwin

package pywasi

import "golang.org/x/sys/unix"

// sockoptName maps a socket option to its host level and name.
func sockoptName(opt int32) (level, name int, ok bool) {
	switch opt {
	case optReuseAddr:
		return unix.SOL_SOCKET, unix.SO_REUSEADDR, true
	case optKeepAlive:
		return unix.SOL_SOCKET, unix.SO_KEEPALIVE, true
	case optRcvBuf:
		return unix.SOL_SOCKET, unix.SO_RCVBUF, true
	case optSndBuf:
		return unix.SOL_SOCKET, unix.SO_SNDBUF, true
	case optReusePort:
		return unix.SOL_SOCKET, unix.SO_REUSEPORT, true
	case optBroadcast:
		return unix.SOL_SOCKET, unix.SO_BROADCAST, true
	case optNoDelay:
		return unix.IPPROTO_TCP, unix.TCP_NODELAY, true
	case optV6Only:
		return unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, true
	}
	return 0, 0, false
}

func getSockopt(fd uintptr, opt int32) (int32, int32) {
	level, name, ok := sockoptName(opt)
	if !ok {
		return 0, errnoNoprotoopt
	}
	v, err := unix.GetsockoptInt(int(fd), level, name)
	if err != nil {
		return 0, errnoOf(err)
	}
	if opt == optReuseAddr || opt == optKeepAlive || opt == optReusePort || opt == optBroadcast || opt == optNoDelay || opt == optV6Only {
		if v != 0 {
			v = 1
		}
	}
	return int32(v), 0
}

func setSockopt(fd uintptr, opt, v int32) int32 {
	level, name, ok := sockoptName(opt)
	if !ok {
		return errnoNoprotoopt
	}
	if err := unix.SetsockoptInt(int(fd), level, name, int(v)); err != nil {
		return errnoOf(err)
	}
	return 0
}
