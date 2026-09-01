//go:build !darwin && !linux

package duck

import (
	"fmt"
	"os"
)

func openNoFollowAt(*os.File, string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow relative open is unsupported on this platform")
}
