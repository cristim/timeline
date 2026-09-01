//go:build darwin || linux

package duck

import (
	"fmt"
	"os"
)

func descriptorPath(file *os.File) string {
	return fmt.Sprintf("/dev/fd/%d", file.Fd())
}
