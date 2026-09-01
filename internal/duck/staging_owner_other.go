//go:build !darwin && !linux

package duck

import "os"

func validateStageOwner(os.FileInfo) error {
	return nil
}
