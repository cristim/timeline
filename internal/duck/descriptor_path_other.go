//go:build !darwin && !linux

package duck

import "os"

func descriptorPath(*os.File) string { return "" }
