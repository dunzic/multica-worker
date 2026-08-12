//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openAndLockRoleSourceConfig(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("role source config lock must be a regular non-symlink file")
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open role source config lock: %w", err)
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrRoleSourceConfigBusy
		}
		return nil, fmt.Errorf("lock role source config: %w", err)
	}
	return file, nil
}

func unlockRoleSourceConfig(file *os.File) {
	if file == nil {
		return
	}
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
	_ = file.Close()
}

func replaceRoleSourceConfig(tempPath, targetPath string) error {
	temp, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(temp, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
