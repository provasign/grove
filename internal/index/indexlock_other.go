//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package index

import (
	"fmt"
	"os"
	"runtime"
)

func tryLockIndexFile(_ *os.File) (bool, error) {
	return false, fmt.Errorf("cross-process index locking is unsupported on %s", runtime.GOOS)
}

func unlockIndexFile(_ *os.File) error { return nil }
