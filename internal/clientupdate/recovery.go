package clientupdate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type recoveryPlan struct {
	Target    string `json:"target"`
	Version   string `json:"version"`
	ParentPID int    `json:"parentPID"`
	Directory string `json:"directory"`
}

func recoveryPointer() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Sonoryx", "pending-update.json"), nil
}

func readRecovery() (recoveryPlan, error) {
	var plan recoveryPlan
	path, err := recoveryPointer()
	if err != nil {
		return plan, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return plan, err
	}
	if err = json.Unmarshal(data, &plan); err != nil {
		return plan, err
	}
	root := filepath.Join(filepath.Dir(path), "update-recovery")
	rel, err := filepath.Rel(root, plan.Directory)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || !filepath.IsAbs(plan.Target) {
		return plan, errors.New("неверный план восстановления обновления")
	}
	return plan, nil
}

// PrepareRecovery starts a watchdog from a copy of the OLD executable. It is
// separate from Wails' swap helper and never enters WebView or SingleInstance.
func PrepareRecovery(version string) (func(), error) {
	cleanupRetiredRecovery()
	pointer, err := recoveryPointer()
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(pointer), "update-recovery", hex.EncodeToString(nonce[:]))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	cleanup := func() { os.Remove(pointer); os.RemoveAll(dir) }
	watcher := filepath.Join(dir, "watcher.exe")
	if err = copyExecutable(self, watcher); err != nil {
		cleanup()
		return nil, err
	}
	plan := recoveryPlan{self, version, os.Getpid(), dir}
	data, _ := json.Marshal(plan)
	if err = os.WriteFile(pointer, data, 0600); err != nil {
		cleanup()
		return nil, err
	}
	if err = startDetached(watcher, append(os.Environ(), "SONORYX_UPDATE_WATCHER=1")); err != nil {
		cleanup()
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(dir, "watcher.ready")); err == nil {
			return cleanup, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	cleanup()
	return nil, errors.New("процесс восстановления не подтвердил готовность")
}

func copyExecutable(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func HandleRecoveryMode() {
	if os.Getenv("SONORYX_UPDATE_WATCHER") != "1" {
		return
	}
	os.Unsetenv("SONORYX_UPDATE_WATCHER")
	os.Exit(runRecoveryWatchdog())
}

func runRecoveryWatchdog() (result int) {
	plan, err := readRecovery()
	if err != nil {
		return 1
	}
	self, _ := os.Executable()
	if !strings.EqualFold(self, filepath.Join(plan.Directory, "watcher.exe")) {
		return 1
	}
	defer func() {
		if result == 0 {
			_ = os.WriteFile(filepath.Join(plan.Directory, "retired"), nil, 0600)
		}
	}()
	if err = os.WriteFile(filepath.Join(plan.Directory, "watcher.ready"), nil, 0600); err != nil {
		return 1
	}
	deadline := time.Now().Add(2 * time.Minute)
	for processAlive(plan.ParentPID) && time.Now().Before(deadline) {
		if _, err = readRecovery(); err != nil {
			return 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	if processAlive(plan.ParentPID) {
		return 0 // Original client stayed open; no swap took place.
	}
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(plan.Directory, "healthy")); err == nil {
			return 0
		}
		if _, err = readRecovery(); err != nil {
			return 0
		}
		time.Sleep(300 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(plan.Directory, "healthy")); err == nil {
		return 0
	}
	// Stop only the replacement process recorded by that executable, after
	// verifying its image path through the same process handle used to stop it.
	pidData, _ := os.ReadFile(filepath.Join(plan.Directory, "new-process.json"))
	var pid int
	if json.Unmarshal(pidData, &pid) == nil {
		if !stopReplacement(pid, plan.Target) {
			return 1
		}
	}
	// Wails removes its .bak immediately after CreateProcess succeeds. Keep
	// our OLD watcher executable as the independent recovery copy until the
	// replacement actually confirms a functioning frontend.
	oldHash, oldErr := executableDigest(self)
	newHash, newErr := executableDigest(plan.Target)
	if oldErr != nil {
		return 1 // Keep the backup if its contents could not be read.
	}
	if newErr != nil || oldHash != newHash {
		err = restoreExecutable(self, plan.Target, filepath.Base(plan.Directory))
		pointer, _ := recoveryPointer()
		os.Remove(pointer)
		message := "Новая версия не подтвердила запуск; восстановлена предыдущая сборка."
		if err != nil {
			message = "Автоматическое восстановление не удалось: " + err.Error() + ". Резервная копия: " + self
		}
		_ = os.WriteFile(filepath.Join(filepath.Dir(pointer), "update-recovery-result.txt"), []byte(message), 0600)
		if err != nil {
			return 1 // Retain the recovery copy for manual restoration.
		}
		if err = startDetached(plan.Target, os.Environ()); err != nil {
			return 1
		}
	} else {
		// The swap helper already restored the original executable.
		pointer, _ := recoveryPointer()
		os.Remove(pointer)
	}
	return 0
}

// A running Windows executable cannot remove itself. The next normal launch
// or update attempt removes only copies explicitly retired by their watchdog.
func cleanupRetiredRecovery() {
	pointer, err := recoveryPointer()
	if err != nil {
		return
	}
	root := filepath.Join(filepath.Dir(pointer), "update-recovery")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	active, _ := readRecovery()
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 32 {
			continue
		}
		if _, err := hex.DecodeString(entry.Name()); err != nil {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel != entry.Name() || filepath.IsAbs(rel) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "retired")); err == nil {
			if err = os.RemoveAll(dir); err == nil && strings.EqualFold(active.Directory, dir) {
				_ = os.Remove(pointer)
			}
		}
	}
}

func executableDigest(path string) ([32]byte, error) {
	var result [32]byte
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return result, err
	}
	copy(result[:], h.Sum(nil))
	return result, nil
}

func restoreExecutable(old, target, nonce string) error {
	staged := target + ".rollback-" + nonce
	failed := target + ".failed-" + nonce
	if err := copyExecutable(old, staged); err != nil {
		return err
	}
	defer os.Remove(staged)
	var last error
	for i := 0; i < 20; i++ {
		moved := false
		if _, err := os.Stat(target); err == nil {
			if err = os.Rename(target, failed); err != nil {
				last = err
				time.Sleep(500 * time.Millisecond)
				continue
			}
			moved = true
		}
		if err := os.Rename(staged, target); err == nil {
			if moved {
				os.Remove(failed)
			}
			return nil
		} else {
			last = err
		}
		if moved {
			if err := os.Rename(failed, target); err != nil {
				return err
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

func RecordRecoveryStartup(version string) {
	cleanupRetiredRecovery()
	plan, err := readRecovery()
	if err != nil {
		return
	}
	if _, err = os.Stat(filepath.Join(plan.Directory, "healthy")); err == nil {
		pointer, _ := recoveryPointer()
		os.Remove(pointer)
		os.RemoveAll(plan.Directory)
		return
	}
	self, _ := os.Executable()
	if !strings.EqualFold(self, plan.Target) || version != plan.Version {
		return
	}
	data, _ := json.Marshal(os.Getpid())
	_ = os.WriteFile(filepath.Join(plan.Directory, "new-process.json"), data, 0600)
}

func ConfirmRecoveryStartup(version string) {
	plan, err := readRecovery()
	if err != nil {
		return
	}
	self, _ := os.Executable()
	if !strings.EqualFold(self, plan.Target) || version != plan.Version {
		return
	}
	_ = os.WriteFile(filepath.Join(plan.Directory, "healthy"), nil, 0600)
}
