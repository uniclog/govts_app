//go:build !windows

package clientupdate

import "errors"

func startDetached(string, []string) error {
	return errors.New("процесс восстановления обновления есть только на Windows")
}
func processAlive(int) bool            { return false }
func stopReplacement(int, string) bool { return false }
