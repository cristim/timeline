//go:build !darwin && !linux

package duck

import (
	"fmt"
	"os"
)

func openDirectoryNoFollow(string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow directory open is unsupported on this platform")
}
