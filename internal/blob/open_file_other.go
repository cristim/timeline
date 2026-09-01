//go:build !darwin && !linux

package blob

import (
	"fmt"
	"os"
)

func openFileNoFollow(string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow file open is unsupported on this platform")
}
