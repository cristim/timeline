//go:build darwin || linux

package duck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDirectoryNoReplaceMovesDirectoryRelativeToFDs(t *testing.T) {
	t.Parallel()

	oldParentPath := t.TempDir()
	newParentPath := t.TempDir()
	oldParent := openTestDir(t, oldParentPath)
	newParent := openTestDir(t, newParentPath)
	if err := os.Mkdir(filepath.Join(oldParentPath, "stage"), 0o700); err != nil {
		t.Fatalf("mkdir staged directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldParentPath, "stage", "model.duckdb"), []byte("model"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}

	want, err := os.Stat(filepath.Join(oldParentPath, "stage"))
	if err != nil {
		t.Fatalf("stat staged directory: %v", err)
	}
	if err := installDirectoryNoReplace(int(oldParent.Fd()), "stage", want, int(newParent.Fd()), "model", nil); err != nil {
		t.Fatalf("installDirectoryNoReplace: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(oldParentPath, "stage")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged directory still exists: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(newParentPath, "model", "model.duckdb"))
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if string(got) != "model" {
		t.Fatalf("installed file = %q, want model", got)
	}
}

func TestInstallDirectoryNoReplaceLeavesExistingTargetUntouched(t *testing.T) {
	t.Parallel()

	oldParentPath := t.TempDir()
	newParentPath := t.TempDir()
	oldParent := openTestDir(t, oldParentPath)
	newParent := openTestDir(t, newParentPath)
	if err := os.Mkdir(filepath.Join(oldParentPath, "stage"), 0o700); err != nil {
		t.Fatalf("mkdir staged directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldParentPath, "stage", "source"), []byte("source"), 0o600); err != nil {
		t.Fatalf("write source marker: %v", err)
	}
	if err := os.Mkdir(filepath.Join(newParentPath, "model"), 0o700); err != nil {
		t.Fatalf("mkdir existing target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newParentPath, "model", "target"), []byte("target"), 0o600); err != nil {
		t.Fatalf("write target marker: %v", err)
	}

	want, err := os.Stat(filepath.Join(oldParentPath, "stage"))
	if err != nil {
		t.Fatalf("stat staged directory: %v", err)
	}
	if err := installDirectoryNoReplace(int(oldParent.Fd()), "stage", want, int(newParent.Fd()), "model", nil); err == nil {
		t.Fatal("installDirectoryNoReplace succeeded over existing target")
	}
	if got, err := os.ReadFile(filepath.Join(oldParentPath, "stage", "source")); err != nil || string(got) != "source" {
		t.Fatalf("source marker = %q, err %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(newParentPath, "model", "target")); err != nil || string(got) != "target" {
		t.Fatalf("target marker = %q, err %v", got, err)
	}
}

func TestInstallDirectoryNoReplaceRefusesReplacedSource(t *testing.T) {
	t.Parallel()

	oldParentPath := t.TempDir()
	newParentPath := t.TempDir()
	oldParent := openTestDir(t, oldParentPath)
	newParent := openTestDir(t, newParentPath)
	stage := filepath.Join(oldParentPath, "stage")
	displaced := stage + ".displaced"
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatalf("mkdir staged directory: %v", err)
	}
	want, err := os.Stat(stage)
	if err != nil {
		t.Fatalf("stat staged directory: %v", err)
	}
	if err := os.Rename(stage, displaced); err != nil {
		t.Fatalf("displace staged directory: %v", err)
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatalf("replace staged directory: %v", err)
	}

	if err := installDirectoryNoReplace(int(oldParent.Fd()), "stage", want, int(newParent.Fd()), "model", nil); err == nil {
		t.Fatal("installDirectoryNoReplace accepted replaced source")
	}
	for _, path := range []string{stage, displaced} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("source race entry %s: info=%v err=%v", path, info, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(newParentPath, "model")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement installed: %v", err)
	}
}

func TestInstallDirectoryNoReplaceRestoresReplacementRacedAfterVerification(t *testing.T) {
	t.Parallel()

	oldParentPath := t.TempDir()
	newParentPath := t.TempDir()
	oldParent := openTestDir(t, oldParentPath)
	newParent := openTestDir(t, newParentPath)
	stage := filepath.Join(oldParentPath, "stage")
	displaced := stage + ".displaced"
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatalf("mkdir staged directory: %v", err)
	}
	want, err := os.Stat(stage)
	if err != nil {
		t.Fatalf("stat staged directory: %v", err)
	}
	var hookErr error
	err = installDirectoryNoReplace(int(oldParent.Fd()), "stage", want, int(newParent.Fd()), "model", func() {
		if hookErr = os.Rename(stage, displaced); hookErr == nil {
			hookErr = os.Mkdir(stage, 0o700)
		}
	})
	if hookErr != nil {
		t.Fatalf("replace verified source: %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("installDirectoryNoReplace error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(newParentPath, "model")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched source remains installed: %v", err)
	}
	for _, path := range []string{stage, displaced} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("source race entry %s: info=%v err=%v", path, info, err)
		}
	}
}

func openTestDir(t *testing.T, path string) *os.File {
	t.Helper()

	dir, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	})
	return dir
}
