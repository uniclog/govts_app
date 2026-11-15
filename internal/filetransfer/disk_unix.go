//go:build !windows

package filetransfer

import (
	"golang.org/x/sys/unix"
	"os/exec"
)

func freeBytes(path string) (uint64, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	return uint64(s.Bavail) * uint64(s.Bsize), err
}
func hideProcess(cmd *exec.Cmd) {}
