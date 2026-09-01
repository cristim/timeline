//go:build darwin || linux

package duck

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func verifyInstallSource(dirFD int, name string, want os.FileInfo) error {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open install source %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return fmt.Errorf("stat install source %s: %w", name, statErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close install source %s: %w", name, closeErr)
	}
	if want == nil || !os.SameFile(want, info) {
		return fmt.Errorf("install source %s identity changed", name)
	}
	return nil
}
