//go:build darwin || linux

package duck

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory without following links: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
