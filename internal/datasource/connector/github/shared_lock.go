package github

import (
	"context"
	"errors"
	"os"
	"time"
)

// mirrorLock lives beside, never inside, the bare repository. Removing a bare
// repository must not replace the lock inode while another process holds it.
func mirrorLock(ctx context.Context, path string, exclusive bool) (func(), error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("GitHub shared cache lock is invalid")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("GitHub shared cache lock is unavailable")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.New("GitHub shared cache lock is unavailable")
	}
	for {
		locked, err := tryMirrorFileLock(f, exclusive)
		if err != nil {
			_ = f.Close()
			return nil, errors.New("GitHub shared cache lock failed")
		}
		if locked {
			if err := ctx.Err(); err != nil {
				_ = unlockMirrorFile(f)
				_ = f.Close()
				return nil, err
			}
			return func() { _ = unlockMirrorFile(f); _ = f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
