//go:build !(linux || darwin)

package wasisock

import (
	"context"
	"net/netip"
	"syscall"

	"github.com/ChrisRx/wazero/api"
)

func dgramSend(context.Context, api.Module, syscall.Conn, []byte, netip.AddrPort, int32) (int, int32) {
	return 0, errnoNosys
}

func dgramRecv(context.Context, api.Module, syscall.Conn, []byte, int32) (int, netip.AddrPort, int32) {
	return 0, netip.AddrPort{}, errnoNosys
}

func connectRaw(syscall.Conn, netip.AddrPort) int32 { return errnoNosys }

func peerAddr(syscall.Conn) (netip.AddrPort, int32) { return netip.AddrPort{}, errnoNosys }
