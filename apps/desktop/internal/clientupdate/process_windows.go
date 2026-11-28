//go:build windows

package clientupdate

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"strings"
	"syscall"
)

func startDetached(path string, env []string) error {
	cmd := exec.Command(path)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259
}

func stopReplacement(pid int, path string) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return !processAlive(pid)
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) != nil || !strings.EqualFold(windows.UTF16ToString(buf[:size]), path) {
		return false
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return false
	}
	result, err := windows.WaitForSingleObject(h, 5000)
	return err == nil && result == windows.WAIT_OBJECT_0
}
