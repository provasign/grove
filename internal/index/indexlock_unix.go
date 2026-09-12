//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package index

import (
	"os"

	"golang.org/x/sys/unix"
)

func tryLockIndexFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
		return false, nil
	}
	return false, err
}

func unlockIndexFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
