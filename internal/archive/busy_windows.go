//go:build windows

package archive

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

func isPlatformBusyError(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}

func renameOwnedFile(source, destination string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = os.Rename(source, destination)
		if err == nil {
			return nil
		}
		if !isPlatformBusyError(err) && !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}
