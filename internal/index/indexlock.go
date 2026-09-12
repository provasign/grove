package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const indexLockRetry = 25 * time.Millisecond

// acquireIndexLock serializes the complete read/parse/analyze/write cycle
// across Grove processes sharing a repository. SQLite serializes individual
// writes, but that is insufficient: two indexers can otherwise compute from
// different snapshots and let the last edge-table replacement win.
func acquireIndexLock(ctx context.Context, root string) (func() error, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat index root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("index root is not a directory: %s", root)
	}
	lockDir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create index lock directory: %w", err)
	}
	path := filepath.Join(lockDir, "index.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open index lock: %w", err)
	}
	for {
		locked, lockErr := tryLockIndexFile(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock index: %w", lockErr)
		}
		if locked {
			return func() error {
				return errors.Join(unlockIndexFile(file), file.Close())
			}, nil
		}
		timer := time.NewTimer(indexLockRetry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, fmt.Errorf("wait for index lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
