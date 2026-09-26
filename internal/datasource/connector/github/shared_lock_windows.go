//go:build windows

package github

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryMirrorFileLock(f *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var overlap windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlap)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlockMirrorFile(f *os.File) error {
	// UnlockFileEx matches the handle and byte range. A fresh zero-offset
	// OVERLAPPED describes the same one-byte range used by LockFileEx.
	var overlap windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap)
}
