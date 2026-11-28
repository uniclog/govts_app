//go:build windows

package filetransfer

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
)

func freeBytes(path string) (uint64, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return 0, e
	}
	var free, total, all uint64
	e = windows.GetDiskFreeSpaceEx(p, &free, &total, &all)
	return free, e
}
func hideProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
