//go:build darwin

package duck

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func installDirectoryNoReplace(olddirfd int, oldname string, want os.FileInfo, newdirfd int, newname string, afterVerify func()) error {
	if err := verifyInstallSource(olddirfd, oldname, want); err != nil {
		return err
	}
	if afterVerify != nil {
		afterVerify()
	}
	if err := unix.RenameatxNp(olddirfd, oldname, newdirfd, newname, unix.RENAME_EXCL); err != nil {
		return err
	}
	if err := verifyInstallSource(newdirfd, newname, want); err != nil {
		rollbackErr := unix.RenameatxNp(newdirfd, newname, olddirfd, oldname, unix.RENAME_EXCL)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore mismatched install source: %w", rollbackErr))
		}
		return err
	}
	return nil
}
