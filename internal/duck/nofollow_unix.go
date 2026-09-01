//go:build darwin || linux

package duck

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openNoFollowAt(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s without following links: %w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}
