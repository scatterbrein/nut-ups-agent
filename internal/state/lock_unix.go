//go:build linux || darwin

package state

import (
	"errors"
	"os"
	"syscall"
)

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open agent lock")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another agent is already using this state directory")
	}
	return f, nil
}
