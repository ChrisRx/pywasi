//go:build !(linux || darwin)

package wasisock

func getSockopt(uintptr, int32) (int32, int32) { return 0, errnoNoprotoopt }

func setSockopt(uintptr, int32, int32) int32 { return errnoNoprotoopt }
