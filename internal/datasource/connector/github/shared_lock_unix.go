//go:build !windows

package github

import (
	"errors"
	"os"
	"syscall"
)

func tryMirrorFileLock(f *os.File, exclusive bool) (bool, error) {
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), mode)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockMirrorFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
