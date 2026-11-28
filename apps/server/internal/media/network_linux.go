package media

import "syscall"

// Linux doubles SO_RCVBUF for bookkeeping overhead and reports the doubled
// value; halve it so the result is comparable with the SetReadBuffer request.
func socketReadBuffer(conn any) (int, bool) {
	raw, ok := conn.(syscall.Conn)
	if !ok {
		return 0, false
	}
	control, err := raw.SyscallConn()
	if err != nil {
		return 0, false
	}
	size, sockErr := 0, error(nil)
	if err := control.Control(func(fd uintptr) {
		size, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil || sockErr != nil {
		return 0, false
	}
	return size / 2, true
}
