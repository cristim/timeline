//go:build !darwin && !linux

package duck

import "errors"
import "os"

func installDirectoryNoReplace(int, string, os.FileInfo, int, string, func()) error {
	return errors.ErrUnsupported
}
