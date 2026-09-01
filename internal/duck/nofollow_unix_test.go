//go:build darwin || linux

package duck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenNoFollowAtRejectsSymlink(t *testing.T) {
	t.Parallel()

	dirPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirPath, "target"), []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink("target", filepath.Join(dirPath, "artifact")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	dir := openTestDir(t, dirPath)
	if file, err := openNoFollowAt(dir, "artifact"); err == nil {
		_ = file.Close()
		t.Fatal("openNoFollowAt followed symlink")
	}
}
