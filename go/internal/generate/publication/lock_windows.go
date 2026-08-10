//go:build windows

package publication

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockGenerationFile(file *os.File) (func() error, error) {
	overlapped := new(windows.Overlapped)
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		return nil, err
	}
	return func() error {
		return windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
	}, nil
}
