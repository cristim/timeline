//go:build darwin || linux

package duck

import (
	"fmt"
	"os"
	"syscall"
)

func validateStageOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat has type %T", info.Sys())
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("uid is %d, want %d", stat.Uid, os.Geteuid())
	}
	return nil
}
