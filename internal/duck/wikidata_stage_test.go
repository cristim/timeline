package duck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wk/internal/ingest"
	"wk/internal/model"
)

func TestWikidataDumpStagingSinkMatchesCompatibilityBytes(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../ingest/testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	compatibility, err := ingest.ImportWikidataDump(strings.NewReader(string(body)), ingest.WikidataDumpImportOptions{})
	if err != nil {
		t.Fatalf("compatibility import: %v", err)
	}
	reportBody, err := json.Marshal(compatibility.Report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	legacyDir := t.TempDir()
	legacyModel, err := WriteModel(context.Background(), legacyDir, compatibility.Entities)
	if err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	legacyReject, err := WriteRejects(context.Background(), legacyDir, rejectRowsForStageTest(compatibility.Rejects))
	if err != nil {
		t.Fatalf("WriteRejects: %v", err)
	}
	legacyReport := filepath.Join(legacyDir, "import.json")
	if err := os.WriteFile(legacyReport, reportBody, 0o644); err != nil {
		t.Fatalf("write legacy report: %v", err)
	}

	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	t.Cleanup(func() {
		if err := stage.Abort(); err != nil {
			t.Errorf("Abort: %v", err)
		}
	})
	report, err := ingest.ImportWikidataDumpToSink(strings.NewReader(string(body)), ingest.WikidataDumpImportOptions{}, stage)
	if err != nil {
		t.Fatalf("stream import: %v", err)
	}
	streamedReportBody, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal streamed report: %v", err)
	}
	if err := stage.Finalize(context.Background(), streamedReportBody); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	legacy := append(append([]ModelFile{}, legacyModel...), legacyReject)
	streamed := finalizedModelFiles(finalOut, stage)
	if len(streamed) != len(legacy) {
		t.Fatalf("streamed files = %d, want %d", len(streamed), len(legacy))
	}
	for i := range legacy {
		if streamed[i].Name != legacy[i].Name || streamed[i].Rows != legacy[i].Rows {
			t.Fatalf("file %d = %#v, want name/rows from %#v", i, streamed[i], legacy[i])
		}
		assertSameFileBytes(t, streamed[i].Path, legacy[i].Path)
	}
	assertSameFileBytes(t, filepath.Join(finalOut, importReportFileName), legacyReport)
	assertFinalizedManifest(t, finalOut, streamed, streamedReportBody)
	if sha256.Sum256(streamedReportBody) != sha256.Sum256(reportBody) {
		t.Fatal("streamed import report changes the model id")
	}
	assertStageDataHandlesClosed(t, stage)
	assertWorkspaceHandlesClosed(t, stage.workspace)
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort finalized stage: %v", err)
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("finalized private stage remains after abort: %v", err)
	}
}

func TestWikidataDumpStagingSinkUsesPrivateScratchAndCleansIt(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	finalOut := filepath.Join(parent, "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	info, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("stat private stage: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private stage mode = %04o, want 0700", info.Mode().Perm())
	}
	for _, name := range []string{"scratch/duck", "scratch/exports", "ready"} {
		child, err := os.Stat(filepath.Join(privatePath, name))
		if err != nil || !child.IsDir() {
			t.Fatalf("private stage child %s: info=%v err=%v", name, child, err)
		}
	}
	if _, err := os.Lstat(finalOut); !os.IsNotExist(err) {
		t.Fatalf("final output exists during staging: %v", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains after abort: %v", err)
	}
	if _, err := os.Lstat(finalOut); !os.IsNotExist(err) {
		t.Fatalf("final output exists after abort: %v", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("second Abort: %v", err)
	}
}

func TestPrivateStageAllocatorLeavesBeforeFirstOpenReplacementUntouched(t *testing.T) {
	t.Parallel()

	parentPath := t.TempDir()
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	var displaced, replacement string
	var hookErr error
	name, dir, root, _, err := createPrivateStageDirectoryWithHook(parent, func(name string) {
		replacement = filepath.Join(parentPath, name)
		displaced = replacement + ".displaced"
		if renameErr := os.Rename(replacement, displaced); renameErr != nil {
			hookErr = renameErr
			return
		}
		hookErr = os.Mkdir(replacement, 0o700)
	})
	if hookErr != nil {
		t.Fatalf("replace private stage during allocation: %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("allocator result name=%q dir=%v root=%v err=%v", name, dir, root, err)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("allocator error %q does not identify stage %q", err, name)
	}
	if dir != nil || root != nil {
		t.Fatalf("allocator returned handles after failed handoff: dir=%v root=%v", dir, root)
	}
	for _, path := range []string{replacement, displaced} {
		if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("allocator removed %s: info=%v err=%v", path, info, statErr)
		}
	}
}

func TestWikidataDumpStagingSinkLeavesExistingOutputUntouched(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	finalOut := filepath.Join(parent, "model")
	want := []byte("competitor")
	if err := os.WriteFile(finalOut, want, 0o600); err != nil {
		t.Fatalf("create competing output: %v", err)
	}
	if _, err := NewWikidataDumpStagingSink(context.Background(), finalOut); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("constructor error = %v", err)
	}
	got, err := os.ReadFile(finalOut)
	if err != nil {
		t.Fatalf("read competing output: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("competing output changed to %q", got)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read output parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "model" {
		t.Fatalf("output parent entries = %v", entries)
	}
}

func TestWikidataDumpStagingSinkFinalizeLeavesCompetingOutputUntouched(t *testing.T) {
	for _, kind := range []string{"file", "empty-directory", "nonempty-directory"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			finalOut := filepath.Join(parent, "model")
			stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			privatePath := stage.workspace.privatePath
			switch kind {
			case "file":
				err = os.WriteFile(finalOut, []byte("competitor"), 0o600)
			case "empty-directory", "nonempty-directory":
				err = os.Mkdir(finalOut, 0o700)
				if err == nil && kind == "nonempty-directory" {
					err = os.WriteFile(filepath.Join(finalOut, "marker"), []byte("competitor"), 0o600)
				}
			}
			if err != nil {
				t.Fatalf("create competitor: %v", err)
			}
			if err := stage.Finalize(context.Background(), []byte("{}")); err == nil {
				t.Fatal("Finalize succeeded over competitor")
			}
			if _, err := os.Lstat(finalOut); err != nil {
				t.Fatalf("competitor removed: %v", err)
			}
			switch kind {
			case "file":
				if got, _ := os.ReadFile(finalOut); string(got) != "competitor" {
					t.Fatalf("competitor file = %q", got)
				}
			case "nonempty-directory":
				if got, _ := os.ReadFile(filepath.Join(finalOut, "marker")); string(got) != "competitor" {
					t.Fatalf("competitor marker = %q", got)
				}
			}
			if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private stage remains after handled install conflict: %v", err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkFinalizeRefusesReplacedReadyDirectory(t *testing.T) {
	parent := t.TempDir()
	finalOut := filepath.Join(parent, "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	ready := filepath.Join(stage.workspace.privatePath, "ready")
	displaced := ready + ".displaced"
	stage.beforeInstall = func() {
		if _, err := os.Lstat(finalOut); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("final output exists before commit: %v", err)
			return
		}
		if err := os.Rename(ready, displaced); err != nil {
			t.Errorf("displace ready: %v", err)
			return
		}
		if err := os.Mkdir(ready, 0o700); err != nil {
			t.Errorf("replace ready: %v", err)
		}
	}
	err = stage.Finalize(context.Background(), []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Finalize error = %v", err)
	}
	if _, err := os.Lstat(finalOut); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final output exists: %v", err)
	}
	for _, path := range []string{ready, displaced} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("ready race entry %s: info=%v err=%v", path, info, err)
		}
	}
}

func TestWikidataDumpStagingSinkFinalizeFailureBoundaries(t *testing.T) {
	boundaries := []string{
		"close-scratch-database",
		"scratch-copy:entity.parquet", "scratch-copy:entity_category.parquet",
		"scratch-copy:relationship.parquet", "scratch-copy:claim.parquet", "scratch-copy:reject.parquet",
		"copy-ready:entity.parquet", "sync-ready:entity.parquet",
		"copy-ready:entity_category.parquet", "sync-ready:entity_category.parquet",
		"copy-ready:relationship.parquet", "sync-ready:relationship.parquet",
		"copy-ready:claim.parquet", "sync-ready:claim.parquet",
		"copy-ready:reject.parquet", "sync-ready:reject.parquet",
		"copy-ready:import.json", "sync-ready:import.json",
		"write-ready:model-files.json", "sync-ready:model-files.json",
		"teardown-scratch", "sync-ready-directory", "install-directory",
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			parent := t.TempDir()
			finalOut := filepath.Join(parent, "model")
			stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			privatePath := stage.workspace.privatePath
			stage.finalizeHook = func(got string) error {
				if got == boundary {
					return errors.New("injected")
				}
				return nil
			}
			err = stage.Finalize(context.Background(), []byte("{}"))
			if err == nil || !strings.Contains(err.Error(), boundary) {
				t.Fatalf("Finalize error = %v", err)
			}
			if _, err := os.Lstat(finalOut); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final output exists after %s: %v", boundary, err)
			}
			if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private stage remains after %s: %v", boundary, err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkFinalizeJoinsPostCommitFailures(t *testing.T) {
	parent := t.TempDir()
	finalOut := filepath.Join(parent, "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	stage.finalizeHook = func(boundary string) error {
		if boundary == "sync-parent-installed" || boundary == "cleanup-private" {
			return errors.New("injected " + boundary)
		}
		return nil
	}
	err = stage.Finalize(context.Background(), []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "sync-parent-installed") || !strings.Contains(err.Error(), "cleanup-private") {
		t.Fatalf("Finalize error = %v", err)
	}
	entries, readErr := os.ReadDir(finalOut)
	if readErr != nil || len(entries) != 7 {
		t.Fatalf("committed output = %v, err %v", entries, readErr)
	}
	if info, statErr := os.Stat(privatePath); statErr != nil || !info.IsDir() {
		t.Fatalf("diagnosed empty private stage = %v, err %v", info, statErr)
	}
	privateEntries, err := os.ReadDir(privatePath)
	if err != nil || len(privateEntries) != 0 {
		t.Fatalf("private stage entries = %v, err %v", privateEntries, err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort after committed error: %v", err)
	}
	if entriesAfter, err := os.ReadDir(finalOut); err != nil || len(entriesAfter) != 7 {
		t.Fatalf("Abort changed committed output: %v, err %v", entriesAfter, err)
	}
}

func TestWikidataDumpStagingSinkFinalizePreservesModelAfterParentSyncFailure(t *testing.T) {
	for _, boundary := range []string{"sync-parent-installed", "sync-parent-cleaned"} {
		t.Run(boundary, func(t *testing.T) {
			parent := t.TempDir()
			finalOut := filepath.Join(parent, "model")
			stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			privatePath := stage.workspace.privatePath
			stage.finalizeHook = func(got string) error {
				if got == boundary {
					return errors.New("injected")
				}
				return nil
			}
			err = stage.Finalize(context.Background(), []byte("{}"))
			if err == nil || !strings.Contains(err.Error(), boundary) {
				t.Fatalf("Finalize error = %v", err)
			}
			if entries, err := os.ReadDir(finalOut); err != nil || len(entries) != 7 {
				t.Fatalf("committed output = %v, err %v", entries, err)
			}
			if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private stage remains: %v", err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkFinalizeCancellationDuringReadyCopy(t *testing.T) {
	for _, cancelAt := range []string{"copy-ready:entity.parquet", "sync-ready:import.json", "sync-ready:model-files.json", "install-directory"} {
		t.Run(cancelAt, func(t *testing.T) {
			parent := t.TempDir()
			finalOut := filepath.Join(parent, "model")
			stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			privatePath := stage.workspace.privatePath
			ctx, cancel := context.WithCancel(context.Background())
			stage.finalizeHook = func(boundary string) error {
				if boundary == cancelAt {
					cancel()
				}
				return nil
			}
			err = stage.Finalize(ctx, []byte("{}"))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Finalize error = %v", err)
			}
			if _, err := os.Lstat(finalOut); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final output exists after cancellation: %v", err)
			}
			if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private stage remains after cancellation: %v", err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkCleanupRefusesReplacedPrivateStage(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(parent, "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	displaced := privatePath + ".displaced"
	if err := os.Rename(privatePath, displaced); err != nil {
		t.Fatalf("displace private stage: %v", err)
	}
	if err := os.Mkdir(privatePath, 0o700); err != nil {
		t.Fatalf("create replacement stage: %v", err)
	}
	marker := filepath.Join(privatePath, "replacement")
	if err := os.WriteFile(marker, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("write replacement marker: %v", err)
	}
	err = stage.Abort()
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Abort error = %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "untouched" {
		t.Fatalf("replacement marker = %q, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(displaced, "scratch", "duck", "model.duckdb")); err != nil {
		t.Fatalf("held stage contents were removed after displacement: %v", err)
	}
}

func TestWikidataDumpStagingSinkFinalizeRefusesReplacedPrivateStage(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	finalOut := filepath.Join(parent, "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	displaced := privatePath + ".displaced"
	if err := os.Rename(privatePath, displaced); err != nil {
		t.Fatalf("displace private stage: %v", err)
	}
	if err := os.Mkdir(privatePath, 0o700); err != nil {
		t.Fatalf("create replacement stage: %v", err)
	}
	marker := filepath.Join(privatePath, "replacement")
	if err := os.WriteFile(marker, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("write replacement marker: %v", err)
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("finalizeScratch error = %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "untouched" {
		t.Fatalf("replacement marker = %q, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(displaced, "scratch", "duck", "model.duckdb")); err != nil {
		t.Fatalf("held stage contents were removed after displacement: %v", err)
	}
	if _, err := os.Lstat(finalOut); !os.IsNotExist(err) {
		t.Fatalf("final output exists after replacement failure: %v", err)
	}
}

func TestWikidataDumpStagingSinkCleanupReportsUnknownResidue(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	unknown := "scratch/duck/unexpected.bin"
	if err := stage.workspace.privateRoot.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unknown residue: %v", err)
	}
	err = stage.Abort()
	if err == nil || !strings.Contains(err.Error(), "remove private stage directory scratch/duck") {
		t.Fatalf("Abort error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(privatePath, unknown))
	if err != nil || string(got) != "keep" {
		t.Fatalf("unknown residue = %q, err %v", got, err)
	}
}

func TestWikidataDumpStagingSinkCleanupRechecksPrivateStageBeforeRemoval(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	displaced := privatePath + ".displaced"
	var hookErr error
	stage.workspace.beforeRemove = func(name string) {
		if name != stagePrivateDirectory {
			return
		}
		if err := os.Rename(privatePath, displaced); err != nil {
			hookErr = err
			return
		}
		hookErr = os.Mkdir(privatePath, 0o700)
	}
	err = stage.Abort()
	if hookErr != nil {
		t.Fatalf("replace private stage before removal: %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Abort error = %v", err)
	}
	if info, err := os.Stat(privatePath); err != nil || !info.IsDir() {
		t.Fatalf("replacement private stage was removed: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(displaced); err != nil || !info.IsDir() {
		t.Fatalf("displaced private stage missing: info=%v err=%v", info, err)
	}
}

func TestWikidataDumpStagingSinkCleanupRefusesReplacedDuckDirectory(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	duckPath := filepath.Join(stage.workspace.privatePath, "scratch", "duck")
	displaced := duckPath + ".displaced"
	if err := os.Rename(duckPath, displaced); err != nil {
		t.Fatalf("displace private DuckDB directory: %v", err)
	}
	if err := os.Mkdir(duckPath, 0o700); err != nil {
		t.Fatalf("create replacement DuckDB directory: %v", err)
	}
	marker := filepath.Join(duckPath, "replacement")
	if err := os.WriteFile(marker, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("write replacement marker: %v", err)
	}
	err = stage.Abort()
	if err == nil || !strings.Contains(err.Error(), "scratch/duck identity changed") {
		t.Fatalf("Abort error = %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "untouched" {
		t.Fatalf("replacement marker = %q, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(displaced, "model.duckdb")); err != nil {
		t.Fatalf("displaced DuckDB was removed during cleanup: %v", err)
	}
}

func TestWikidataDumpStagingSinkCleanupPreservesEveryKnownReplacement(t *testing.T) {
	boundaries := []struct {
		key         string
		directory   bool
		ensureKnown func(*testing.T, *WikidataDumpStagingSink)
	}{
		{key: stageEntityFile},
		{key: stageCategoryFile},
		{key: stageRelationshipFile},
		{key: stageClaimFile},
		{key: stageRejectFile},
		{key: stageImportFile},
		{key: stageDatabaseFile},
		{key: stageDatabaseWALFile, ensureKnown: ensureStageWALForTest},
		{key: stageExportsDirectory, directory: true},
		{key: stageDuckDirectory, directory: true},
		{key: stageScratchDirectory, directory: true},
		{key: stageReadyDirectory, directory: true},
		{key: stagePrivateDirectory, directory: true},
	}
	for _, boundary := range boundaries {
		boundary := boundary
		t.Run(strings.ReplaceAll(boundary.key, "/", "_"), func(t *testing.T) {
			stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			if err := stage.finalizeScratch(context.Background(), []byte("{}")); err != nil {
				t.Fatalf("finalizeScratch: %v", err)
			}
			if boundary.ensureKnown != nil {
				boundary.ensureKnown(t, stage)
			}
			target := stageBoundaryPath(stage.workspace, boundary.key)
			displaced := target + ".displaced"
			var hookErr error
			triggered := false
			stage.workspace.beforeRemove = func(key string) {
				if triggered || key != boundary.key {
					return
				}
				triggered = true
				if err := os.Rename(target, displaced); err != nil {
					hookErr = err
					return
				}
				if boundary.directory {
					hookErr = os.Mkdir(target, 0o700)
				} else {
					hookErr = os.WriteFile(target, []byte("replacement"), 0o600)
				}
			}
			err = stage.Abort()
			if hookErr != nil {
				t.Fatalf("replace %s: %v", boundary.key, hookErr)
			}
			if !triggered {
				t.Fatalf("cleanup did not reach %s", boundary.key)
			}
			if err == nil || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("Abort error = %v", err)
			}
			for _, path := range []string{target, displaced} {
				if _, statErr := os.Lstat(path); statErr != nil {
					t.Fatalf("cleanup removed %s for %s: %v", path, boundary.key, statErr)
				}
			}
			if !boundary.directory {
				if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "replacement" {
					t.Fatalf("replacement %s = %q, err %v", boundary.key, got, readErr)
				}
			}
		})
	}
}

func TestWikidataDumpStagingSinkCleanupPreservesReadyArtifactReplacements(t *testing.T) {
	for _, name := range readyArtifactNames {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(parent, "model"))
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			for _, artifact := range readyArtifactNames {
				file, err := stage.workspace.readyRoot.OpenFile(artifact, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					t.Fatalf("create ready artifact %s: %v", artifact, err)
				}
				if _, err := file.Write([]byte("owned")); err != nil {
					t.Fatalf("write ready artifact %s: %v", artifact, err)
				}
				if err := stage.workspace.recordReadyFile(artifact, file); err != nil {
					t.Fatalf("record ready artifact %s: %v", artifact, err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("close ready artifact %s: %v", artifact, err)
				}
			}
			readyPath := filepath.Join(stage.workspace.privatePath, "ready")
			original := filepath.Join(readyPath, name)
			displaced := original + ".displaced"
			var hookErr error
			stage.workspace.beforeRemove = func(key string) {
				if key != "ready/"+name {
					return
				}
				if hookErr = os.Rename(original, displaced); hookErr == nil {
					hookErr = os.WriteFile(original, []byte("replacement"), 0o600)
				}
			}
			err = stage.Abort()
			if hookErr != nil {
				t.Fatalf("replace ready artifact: %v", hookErr)
			}
			if err == nil || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("Abort error = %v", err)
			}
			if got, err := os.ReadFile(original); err != nil || string(got) != "replacement" {
				t.Fatalf("replacement = %q, err %v", got, err)
			}
			if got, err := os.ReadFile(displaced); err != nil || string(got) != "owned" {
				t.Fatalf("displaced original = %q, err %v", got, err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkRejectsDuplicateQIDsBeforeAndAfterFiltering(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "filtered", input: duplicateStageInput(false)},
		{name: "accepted", input: duplicateStageInput(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			finalOut := filepath.Join(t.TempDir(), "model")
			stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			privatePath := stage.workspace.privatePath
			_, err = ingest.ImportWikidataDumpToSink(strings.NewReader(test.input), ingest.WikidataDumpImportOptions{}, stage)
			if err == nil || !strings.Contains(err.Error(), "duplicate wikidata id Q9401") {
				t.Fatalf("duplicate error = %v", err)
			}
			if err := stage.Abort(); err != nil {
				t.Fatalf("Abort: %v", err)
			}
			if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
				t.Fatalf("private stage remains after import failure: %v", err)
			}
			if _, err := os.Lstat(finalOut); !os.IsNotExist(err) {
				t.Fatalf("final output exists after import failure: %v", err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkMatchesGlobalSlugCollisions(t *testing.T) {
	t.Parallel()

	entities := []*model.Entity{
		{SeedID: "a", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1800), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 1},
		{SeedID: "b", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1900), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 0.5},
		{SeedID: "c", Name: "Alpha 1900", Type: "event", T0: model.YearToSeconds(1900), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 1},
	}
	want := cloneStageEntities(entities)
	wantErr := model.AssignSlugs(want)
	if wantErr == nil {
		t.Fatal("fixture does not exercise a global cross-base collision")
	}

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	stage.transactionLimit = 1
	slugCommits := 0
	stage.slugCommitHook = func(boundary string) error {
		if boundary == "after-slug-commit" {
			slugCommits++
		}
		return nil
	}
	t.Cleanup(func() { _ = stage.Abort() })
	for i, entity := range entities {
		if err := stage.MarkSeenQID(fmt.Sprintf("Q%d", i+1)); err != nil {
			t.Fatalf("MarkSeenQID: %v", err)
		}
		if err := stage.AcceptEntity(entity); err != nil {
			t.Fatalf("AcceptEntity: %v", err)
		}
	}
	if err := stage.finalizeScratch(context.Background(), []byte("{}")); err == nil ||
		!strings.Contains(err.Error(), "unresolvable slug collision") {
		t.Fatalf("finalizeScratch error = %v, want %v", err, wantErr)
	}
	if slugCommits == 0 {
		t.Fatal("cross-base collision occurred before any slug batch committed")
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains after failed finalization: %v", err)
	}
}

func TestWikidataDumpStagingSinkMatchesSlugFallbacks(t *testing.T) {
	t.Parallel()

	entities := []*model.Entity{
		{SeedID: "a", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1800), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 1, Wikidata: "Q1"},
		{SeedID: "b", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1900), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 0.8, Wikidata: "Q2"},
		{SeedID: "c", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1900), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 0.7, Wikidata: "Q3"},
		{SeedID: "d", Name: "Alpha", Type: "event", T0: model.YearToSeconds(1900), Precision: "year", Status: "documented", Categories: []string{"science"}, Importance: 0.6, Wikidata: "Q4"},
	}
	want := cloneStageEntities(entities)
	if err := model.AssignSlugs(want); err != nil {
		t.Fatalf("AssignSlugs: %v", err)
	}

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	slugCommits := 0
	stage.slugCommitHook = func(boundary string) error {
		if boundary == "after-slug-commit" {
			slugCommits++
		}
		return nil
	}
	t.Cleanup(func() { _ = stage.Abort() })
	for i, entity := range entities {
		if err := stage.MarkSeenQID(fmt.Sprintf("Q%d", i+1)); err != nil {
			t.Fatalf("MarkSeenQID: %v", err)
		}
		if err := stage.AcceptEntity(entity); err != nil {
			t.Fatalf("AcceptEntity: %v", err)
		}
	}
	if err := stage.finalizeScratch(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("finalizeScratch: %v", err)
	}
	if slugCommits != len(entities) {
		t.Fatalf("slug commits = %d, want %d", slugCommits, len(entities))
	}
	legacyDir := t.TempDir()
	legacy, err := WriteModel(context.Background(), legacyDir, want)
	if err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	for _, file := range legacy {
		assertSameFileBytes(t, filepath.Join(stage.workspace.exportsPath(), file.Name), file.Path)
	}
}

func TestWikidataDumpStagingSinkRejectsEarlyDuplicateAfterThousandsOfMarks(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	for i := range 5_000 {
		if err := stage.MarkSeenQID(fmt.Sprintf("Q%d", i+1)); err != nil {
			t.Fatalf("MarkSeenQID %d: %v", i+1, err)
		}
	}
	if err := stage.MarkSeenQID("Q7"); err == nil || !strings.Contains(err.Error(), "duplicate wikidata id Q7") {
		t.Fatalf("duplicate MarkSeenQID error = %v", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if stage.seenQIDStmt != nil {
		t.Fatal("Abort retained the seen-QID statement")
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains after abort: %v", err)
	}
}

func TestWikidataDumpStagingSinkRotatesCompleteItemsAndRejectsCrossBatchDuplicate(t *testing.T) {
	t.Parallel()

	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	privatePath := stage.workspace.privatePath

	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("mark accepted item: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("accept item: %v", err)
	}
	if err := stage.MarkSeenQID("Q2"); err != nil {
		t.Fatalf("mark rejected item: %v", err)
	}
	if err := stage.Reject(ingest.Reject{Source: ingest.RejectSourceWikidataDump, File: "dump", Line: 2, Reason: "bad"}); err != nil {
		t.Fatalf("reject item: %v", err)
	}
	if err := stage.MarkSeenQID("Q3"); err != nil {
		t.Fatalf("mark filtered item: %v", err)
	}
	if err := stage.MarkSeenQID("Q1"); err == nil || !strings.Contains(err.Error(), "duplicate wikidata id Q1") {
		t.Fatalf("cross-batch duplicate error = %v", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	assertStageDataHandlesClosed(t, stage)
	if err := stage.Abort(); err != nil {
		t.Fatalf("second Abort: %v", err)
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains: %v", err)
	}
	if _, err := os.Lstat(finalOut); !os.IsNotExist(err) {
		t.Fatalf("final output exists: %v", err)
	}
}

func TestWikidataDumpStagingSinkRotationCancellationCleansCommittedPrefix(t *testing.T) {
	t.Parallel()

	for _, boundary := range []string{"before-flush", "after-commit"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			stage, err := NewWikidataDumpStagingSink(ctx, filepath.Join(t.TempDir(), "model"))
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			stage.transactionLimit = 1
			privatePath := stage.workspace.privatePath
			stage.rotationHook = func(got string) error {
				if got == boundary {
					cancel()
				}
				return nil
			}
			if err := stage.MarkSeenQID("Q1"); err != nil {
				t.Fatalf("first MarkSeenQID: %v", err)
			}
			value := 1.0
			entity := testStageEntity("Q1", "Alpha", 1)
			entity.Rel = []model.SeedRel{{Type: "part_of", Target: "Q1"}}
			entity.Props = []model.SeedProp{{
				Property: "count", Value: &value, ValueType: "number", Source: "test", PublishedAt: "2026-09-02",
			}}
			if err := stage.AcceptEntity(entity); err != nil {
				t.Fatalf("AcceptEntity: %v", err)
			}
			if err := stage.Reject(ingest.Reject{Source: ingest.RejectSourceWikidataDump, File: "dump", Line: 1, Reason: "bad"}); err != nil {
				t.Fatalf("Reject: %v", err)
			}
			err = stage.MarkSeenQID("Q2")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("rotation error = %v, want context cancellation", err)
			}
			if boundary == "before-flush" {
				flushFailed := strings.Contains(err.Error(), "flush ") && strings.Contains(err.Error(), " appender")
				if !flushFailed && !strings.Contains(err.Error(), "begin next staging DuckDB transaction") {
					t.Fatalf("rotation error does not identify canceled flush or begin: %v", err)
				}
			}
			abortErr := stage.Abort()
			if boundary == "before-flush" {
				if abortErr != nil && !strings.Contains(abortErr.Error(), "Current transaction is aborted") {
					t.Fatalf("Abort error = %v, want nil or aborted transaction close error", abortErr)
				}
			} else if abortErr != nil {
				t.Fatalf("Abort: %v", abortErr)
			}
			assertStageDataHandlesClosed(t, stage)
			if err := stage.Abort(); err != nil {
				t.Fatalf("second Abort: %v", err)
			}
			if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
				t.Fatalf("private stage remains: %v", err)
			}
		})
	}
}

type stagedCommitErrorTx struct {
	driver.Tx
	delegate      bool
	afterDelegate func()
	err           error
}

func (tx *stagedCommitErrorTx) Commit() error {
	if tx.delegate {
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
		if tx.afterDelegate != nil {
			tx.afterDelegate()
		}
	}
	return tx.err
}

func TestWikidataDumpStagingSinkAmbiguousCommitPreservesNewUnrecordedWAL(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	sentinel := errors.New("ambiguous committed result")
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".displaced"
	var hookErr error
	stage.tx = &stagedCommitErrorTx{
		Tx: stage.tx, delegate: true, err: sentinel,
		afterDelegate: func() {
			if stage.workspace.fileIdentities[stageDatabaseWALFile] != nil {
				hookErr = fmt.Errorf("WAL was already recorded before the real commit")
				return
			}
			if err := os.Rename(walPath, displaced); err != nil {
				hookErr = err
				return
			}
			hookErr = os.WriteFile(walPath, []byte("replacement"), 0o600)
		},
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if hookErr != nil {
		t.Fatalf("replace committed WAL: %v", hookErr)
	}
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "has no recorded identity") {
		t.Fatalf("finalize error = %v", err)
	}
	for _, path := range []string{walPath, displaced} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("ambiguous cleanup removed %s: %v", path, statErr)
		}
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "replacement" {
		t.Fatalf("replacement WAL = %q, err %v", got, readErr)
	}
	if !strings.Contains(err.Error(), "model.duckdb.wal.unowned-") {
		t.Fatalf("finalize error does not report preserved WAL link: %v", err)
	}
}

func TestWikidataDumpStagingSinkAmbiguousRawCommitCleansKnownFiles(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("injected commit result")
	for _, site := range []string{"rotation", "final"} {
		for _, delegate := range []bool{false, true} {
			name := fmt.Sprintf("%s/delegate=%t", site, delegate)
			t.Run(name, func(t *testing.T) {
				stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
				if err != nil {
					t.Fatalf("NewWikidataDumpStagingSink: %v", err)
				}
				privatePath := stage.workspace.privatePath
				if err := stage.MarkSeenQID("Q1"); err != nil {
					t.Fatalf("MarkSeenQID: %v", err)
				}
				if delegate {
					ensureStageWALForTest(t, stage)
				}
				stage.tx = &stagedCommitErrorTx{Tx: stage.tx, delegate: delegate, err: sentinel}
				if site == "rotation" {
					stage.transactionLimit = 1
					err = stage.MarkSeenQID("Q2")
				} else {
					err = stage.finalizeScratch(context.Background(), []byte("{}"))
				}
				if !errors.Is(err, sentinel) {
					t.Fatalf("commit error = %v, want sentinel", err)
				}
				if !stage.commitAmbiguous {
					t.Fatal("commit error did not mark ownership ambiguous")
				}
				if !stage.aborted {
					if err := stage.Abort(); err != nil {
						t.Fatalf("Abort: %v", err)
					}
				}
				assertStageDataHandlesClosed(t, stage)
				if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
					t.Fatalf("private stage remains: %v", err)
				}
			})
		}
	}
}

func TestWikidataDumpStagingSinkAmbiguousCommitPreservesRecordedWALReplacement(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("first MarkSeenQID: %v", err)
	}
	if err := stage.MarkSeenQID("Q2"); err != nil {
		t.Fatalf("rotation MarkSeenQID: %v", err)
	}
	if stage.workspace.fileIdentities[stageDatabaseWALFile] == nil {
		t.Fatal("successful rotation did not record the WAL")
	}

	sentinel := errors.New("ambiguous committed result")
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".displaced"
	var hookErr error
	stage.tx = &stagedCommitErrorTx{
		Tx: stage.tx, delegate: true, err: sentinel,
		afterDelegate: func() {
			if err := os.Rename(walPath, displaced); err != nil {
				hookErr = err
				return
			}
			hookErr = os.WriteFile(walPath, []byte("replacement"), 0o600)
		},
	}
	err = stage.MarkSeenQID("Q3")
	if hookErr != nil {
		t.Fatalf("replace recorded WAL: %v", hookErr)
	}
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("rotation error = %v", err)
	}
	if abortErr := stage.Abort(); abortErr == nil || !strings.Contains(abortErr.Error(), "identity changed") {
		t.Fatalf("Abort error = %v", abortErr)
	}
	for _, path := range []string{walPath, displaced} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("ambiguous cleanup removed %s: %v", path, statErr)
		}
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "replacement" {
		t.Fatalf("replacement WAL = %q, err %v", got, readErr)
	}
}

func TestWikidataDumpStagingSinkSlugCommitBoundariesCleanPrivatePrefixes(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("injected slug boundary")
	for _, test := range []struct {
		name      string
		boundary  string
		cancel    bool
		ambiguous bool
	}{
		{name: "before hook error rolls back", boundary: "before-slug-commit"},
		{name: "canceled real commit is ambiguous", boundary: "before-slug-commit", cancel: true, ambiguous: true},
		{name: "after successful commit", boundary: "after-slug-commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			stage.transactionLimit = 1
			privatePath := stage.workspace.privatePath
			if err := stage.MarkSeenQID("Q1"); err != nil {
				t.Fatalf("MarkSeenQID: %v", err)
			}
			if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
				t.Fatalf("AcceptEntity: %v", err)
			}
			stage.slugCommitHook = func(got string) error {
				if got != test.boundary {
					return nil
				}
				if test.cancel {
					cancel()
					return nil
				}
				return sentinel
			}
			err = stage.finalizeScratch(ctx, []byte("{}"))
			if test.cancel {
				if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "transaction has already been committed or rolled back") {
					t.Fatalf("canceled commit error = %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("hook error = %v, want sentinel", err)
			}
			if stage.commitAmbiguous != test.ambiguous {
				t.Fatalf("commitAmbiguous = %t, want %t", stage.commitAmbiguous, test.ambiguous)
			}
			assertStageDataHandlesClosed(t, stage)
			if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
				t.Fatalf("private stage remains: %v", err)
			}
		})
	}
}

func TestWikidataDumpStagingSinkCanceledSlugCommitPreservesUnrecordedWAL(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	ensureStageWALForTest(t, stage)
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".displaced"
	var hookErr error
	stage.slugCommitHook = func(boundary string) error {
		switch boundary {
		case "before-slug-commit":
			cancel()
		case "after-slug-commit-error":
			delete(stage.workspace.fileIdentities, stageDatabaseWALFile)
			if _, err := os.Lstat(walPath); err == nil {
				if err := os.Rename(walPath, displaced); err != nil {
					hookErr = err
					return nil
				}
			} else if !os.IsNotExist(err) {
				hookErr = err
				return nil
			}
			hookErr = os.WriteFile(walPath, []byte("unrecorded"), 0o600)
		}
		return nil
	}
	err = stage.finalizeScratch(ctx, []byte("{}"))
	if hookErr != nil {
		t.Fatalf("create unrecorded WAL: %v", hookErr)
	}
	if !stage.commitAmbiguous || !strings.Contains(err.Error(), "has no recorded identity") {
		t.Fatalf("canceled slug commit error = %v, ambiguous=%t", err, stage.commitAmbiguous)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "unrecorded" {
		t.Fatalf("unrecorded WAL = %q, err %v", got, readErr)
	}
	if _, statErr := os.Lstat(stage.workspace.privatePath); statErr != nil {
		t.Fatalf("ambiguous cleanup removed private container: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkBatchingPreservesExactModelBytes(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../ingest/testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	build := func(limit int) (map[string][]byte, int) {
		out := filepath.Join(t.TempDir(), "model")
		stage, err := NewWikidataDumpStagingSink(context.Background(), out)
		if err != nil {
			t.Fatalf("NewWikidataDumpStagingSink: %v", err)
		}
		stage.transactionLimit = limit
		slugCommits := 0
		stage.slugCommitHook = func(boundary string) error {
			if boundary == "after-slug-commit" {
				slugCommits++
			}
			return nil
		}
		report, err := ingest.ImportWikidataDumpToSink(strings.NewReader(string(body)), ingest.WikidataDumpImportOptions{}, stage)
		if err != nil {
			t.Fatalf("ImportWikidataDumpToSink: %v", err)
		}
		reportBody, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := stage.Finalize(context.Background(), reportBody); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		artifacts := make(map[string][]byte)
		for _, name := range []string{
			"entity.parquet", "entity_category.parquet", "relationship.parquet", "claim.parquet",
			"reject.parquet", "import.json", "model-files.json",
		} {
			contents, err := os.ReadFile(filepath.Join(out, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			artifacts[name] = contents
		}
		return artifacts, slugCommits
	}

	oneRow, oneRowSlugCommits := build(1)
	production, _ := build(wikidataStageTransactionItemLimit)
	if oneRowSlugCommits < 2 {
		t.Fatalf("one-row slug commits = %d, want multiple batches", oneRowSlugCommits)
	}
	if !reflect.DeepEqual(oneRow, production) {
		for name := range production {
			if !reflect.DeepEqual(oneRow[name], production[name]) {
				t.Errorf("%s differs across transaction limits", name)
			}
		}
	}
}

func TestWikidataDumpStagingSinkPreservesWALReappearingAfterVerifiedAbsence(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous=%t", ambiguous), func(t *testing.T) {
			stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
			if err != nil {
				t.Fatalf("NewWikidataDumpStagingSink: %v", err)
			}
			if err := stage.closeWriters(); err != nil {
				t.Fatalf("closeWriters: %v", err)
			}
			if err := stage.tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			stage.tx = nil
			if err := stage.closeDuckDB(); err != nil {
				t.Fatalf("closeDuckDB: %v", err)
			}
			if err := stage.workspace.verifyRecordedDuckFiles(true); err != nil {
				t.Fatalf("verify closed database: %v", err)
			}
			ensureStageWALForTest(t, stage)
			if err := stage.workspace.duckRoot.Remove("model.duckdb.wal"); err != nil {
				t.Fatalf("remove recorded WAL: %v", err)
			}
			stage.commitAmbiguous = ambiguous
			stage.workspace.beforeRemove = func(key string) {
				if key == stageDatabaseWALFile {
					if err := stage.workspace.duckRoot.WriteFile("model.duckdb.wal", []byte("replacement"), 0o600); err != nil {
						t.Errorf("recreate WAL: %v", err)
					}
				}
			}
			err = stage.Abort()
			if err == nil || !strings.Contains(err.Error(), "has no recorded identity") {
				t.Fatalf("Abort error = %v", err)
			}
			walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
			if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "replacement" {
				t.Fatalf("replacement WAL = %q, err %v", got, readErr)
			}
		})
	}
}

func TestWikidataDumpStagingSinkReportsRequiredDatabaseAbsence(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	if err := stage.closeWriters(); err != nil {
		t.Fatalf("closeWriters: %v", err)
	}
	if err := stage.tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	stage.tx = nil
	if err := stage.closeDuckDB(); err != nil {
		t.Fatalf("closeDuckDB: %v", err)
	}
	if err := stage.workspace.duckRoot.Remove("model.duckdb"); err != nil {
		t.Fatalf("remove database: %v", err)
	}
	stage.commitAmbiguous = true
	err = stage.Abort()
	if err == nil || !strings.Contains(err.Error(), "private stage file "+stageDatabaseFile+" is missing") {
		t.Fatalf("Abort error = %v", err)
	}
}

func TestWikidataDumpStagingSinkDirectAbortAdoptsFilesCreatedDuringRollback(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("ordinary abort retained private stage: %v", err)
	}
}

func TestWikidataDumpStagingSinkCanceledSlugCommitAfterDurableBatchPreservesReplacement(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	t.Cleanup(func() { _ = stage.Abort() })
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".durable-displaced"
	var hookErr error
	var replacementInfo os.FileInfo
	commits := 0
	stage.slugCommitHook = func(boundary string) error {
		switch boundary {
		case "after-slug-commit":
			commits++
		case "before-slug-commit":
			if commits == 1 {
				cancel()
				return nil
			}
		case "after-slug-commit-error":
			if err := os.Rename(walPath, displaced); err != nil {
				hookErr = err
				return nil
			}
			hookErr = os.WriteFile(walPath, []byte("durable-prefix-replacement"), 0o600)
			if hookErr == nil {
				replacementInfo, hookErr = os.Stat(walPath)
			}
		}
		return nil
	}
	for i, entity := range []*model.Entity{
		testStageEntity("Q1", "Alpha", 1),
		testStageEntity("Q2", "Beta", 1),
	} {
		if err := stage.MarkSeenQID(fmt.Sprintf("Q%d", i+1)); err != nil {
			t.Fatalf("MarkSeenQID: %v", err)
		}
		if err := stage.AcceptEntity(entity); err != nil {
			t.Fatalf("AcceptEntity: %v", err)
		}
	}
	err = stage.finalizeScratch(ctx, []byte("{}"))
	if hookErr != nil {
		t.Fatalf("replacement hook: %v", hookErr)
	}
	if commits != 1 || (!errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone)) || !stage.commitAmbiguous {
		t.Fatalf("finalize error = %v, commits = %d", err, commits)
	}
	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat replacement WAL: %v", statErr)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "durable-prefix-replacement" || !os.SameFile(info, replacementInfo) {
		t.Fatalf("replacement WAL = %q, err %v", got, readErr)
	}
	if _, statErr := os.Lstat(stage.workspace.privatePath); statErr != nil {
		t.Fatalf("preserved private container missing: %v", statErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkSlugPrecommitReplacementIsNotAdopted(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	t.Cleanup(func() { _ = stage.Abort() })
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	if err := stage.MarkSeenQID("Q2"); err != nil {
		t.Fatalf("second MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q2", "Beta", 1)); err != nil {
		t.Fatalf("second AcceptEntity: %v", err)
	}
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".slug-precommit-displaced"
	var hookErr error
	var replacementInfo os.FileInfo
	commits := 0
	stage.slugCommitHook = func(boundary string) error {
		if boundary != "after-slug-commit" {
			return nil
		}
		commits++
		if commits != 1 {
			return nil
		}
		ensureStageWALForTest(t, stage)
		if err := os.Rename(walPath, displaced); err != nil {
			hookErr = err
			return nil
		}
		hookErr = os.WriteFile(walPath, []byte("slug-precommit-replacement"), 0o600)
		if hookErr == nil {
			replacementInfo, hookErr = os.Stat(walPath)
		}
		return nil
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if hookErr != nil {
		t.Fatalf("replacement hook: %v", hookErr)
	}
	if commits != 1 || err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("finalize error = %v, commits = %d", err, commits)
	}
	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat replacement WAL: %v", statErr)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "slug-precommit-replacement" || !os.SameFile(info, replacementInfo) {
		t.Fatalf("replacement database = %q, err %v", got, readErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkSlugHookUnknownWALIsNotAdopted(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	t.Cleanup(func() { _ = stage.Abort() })
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	var replacementInfo os.FileInfo
	sentinel := errors.New("ordinary slug hook error")
	var hookErr error
	stage.slugCommitHook = func(boundary string) error {
		if boundary != "before-slug-commit" {
			return nil
		}
		if err := os.WriteFile(walPath, []byte("unknown-wal"), 0o600); err != nil {
			hookErr = err
		} else {
			replacementInfo, hookErr = os.Stat(walPath)
		}
		return sentinel
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if hookErr != nil {
		t.Fatalf("unknown WAL hook: %v", hookErr)
	}
	if !errors.Is(err, sentinel) || stage.commitAmbiguous || !stage.unownedDuckFiles {
		t.Fatalf("finalize error = %v, want ordinary hook error", err)
	}
	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat unknown WAL: %v", statErr)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "unknown-wal" || !os.SameFile(info, replacementInfo) {
		t.Fatalf("unknown WAL = %q, err %v", got, readErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkBeforeFlushReplacementIsPreserved(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	t.Cleanup(func() { _ = stage.Abort() })
	stage.transactionLimit = 1
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("first MarkSeenQID: %v", err)
	}
	if err := stage.MarkSeenQID("Q2"); err != nil {
		t.Fatalf("second MarkSeenQID: %v", err)
	}
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".before-flush-displaced"
	var hookErr error
	var replacementInfo os.FileInfo
	stage.rotationHook = func(boundary string) error {
		if boundary != "before-flush" {
			return nil
		}
		if err := os.Rename(walPath, displaced); err != nil {
			hookErr = err
			return nil
		}
		hookErr = os.WriteFile(walPath, []byte("before-flush-replacement"), 0o600)
		if hookErr == nil {
			replacementInfo, hookErr = os.Stat(walPath)
		}
		return nil
	}
	err = stage.MarkSeenQID("Q3")
	if hookErr != nil {
		t.Fatalf("replacement hook: %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("rotation error = %v", err)
	}
	if abortErr := stage.Abort(); abortErr != nil && !strings.Contains(abortErr.Error(), "identity changed") {
		t.Fatalf("Abort: %v", abortErr)
	}
	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat replacement WAL: %v", statErr)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "before-flush-replacement" || !os.SameFile(info, replacementInfo) {
		t.Fatalf("replacement WAL = %q, err %v", got, readErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkInitialRawCloseReplacementIsPreserved(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	t.Cleanup(func() { _ = stage.Abort() })
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".finalize-close-displaced"
	var hookErr error
	var replacementInfo os.FileInfo
	stage.finalizeHook = func(boundary string) error {
		if boundary != "close-scratch-database" {
			return nil
		}
		if err := os.Rename(walPath, displaced); err != nil {
			hookErr = err
			return nil
		}
		hookErr = os.WriteFile(walPath, []byte("finalize-close-replacement"), 0o600)
		if hookErr == nil {
			replacementInfo, hookErr = os.Stat(walPath)
		}
		return nil
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if hookErr != nil {
		t.Fatalf("replacement hook: %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("finalize error = %v, want identity diagnostic", err)
	}
	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat replacement WAL: %v", statErr)
	}
	if got, readErr := os.ReadFile(walPath); readErr != nil || string(got) != "finalize-close-replacement" || !os.SameFile(info, replacementInfo) {
		t.Fatalf("replacement WAL = %q, err %v", got, readErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestRestorePreservedDuckFilesRejectsReplacedPreservationLink(t *testing.T) {
	w, err := newStagingWorkspace(filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("newStagingWorkspace: %v", err)
	}
	defer func() { _ = w.cleanup() }()
	if err := w.duckRoot.WriteFile("model.duckdb.wal", []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write WAL: %v", err)
	}
	originalInfo, err := w.duckRoot.Lstat("model.duckdb.wal")
	if err != nil {
		t.Fatalf("stat original WAL: %v", err)
	}
	preserved, err := w.preserveUnownedDuckFiles()
	if err != nil || len(preserved) != 1 {
		t.Fatalf("preserve = %#v, err %v", preserved, err)
	}
	if err := w.duckRoot.Remove(preserved[0].preserved); err != nil {
		t.Fatalf("remove preservation link: %v", err)
	}
	if err := w.duckRoot.WriteFile(preserved[0].preserved, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	displaced := preserved[0].name + ".displaced"
	if err := w.duckRoot.Rename(preserved[0].name, displaced); err != nil {
		t.Fatalf("rename fixed path: %v", err)
	}
	err = w.restorePreservedDuckFiles(preserved)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("restore error = %v, want identity diagnostic", err)
	}
	if _, statErr := w.duckRoot.Lstat(preserved[0].name); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fixed path recreated from sentinel: %v", statErr)
	}
	if displacedInfo, statErr := w.duckRoot.Lstat(displaced); statErr != nil || !os.SameFile(displacedInfo, originalInfo) {
		t.Fatalf("displaced original identity = %v, err %v", displacedInfo, statErr)
	}
	if got, readErr := w.duckRoot.ReadFile(displaced); readErr != nil || string(got) != "replacement" {
		t.Fatalf("displaced original bytes = %q, err %v", got, readErr)
	}
	if got, readErr := w.duckRoot.ReadFile(preserved[0].preserved); readErr != nil || string(got) != "sentinel" {
		t.Fatalf("sentinel bytes = %q, err %v", got, readErr)
	}
}

func TestWikidataDumpStagingSinkSlugCommittedHookErrorPreservesWAL(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	stage.transactionLimit = 1
	t.Cleanup(func() { _ = stage.Abort() })
	walPath := filepath.Join(stage.workspace.privatePath, stageDatabaseWALFile)
	displaced := walPath + ".original"
	sentinel := errors.New("ordinary postcommit stop")
	var original os.FileInfo
	stage.slugCommitHook = func(boundary string) error {
		if boundary != "after-slug-commit" {
			return nil
		}
		if err := os.Rename(walPath, displaced); err != nil {
			return err
		}
		if err := os.WriteFile(walPath, []byte("replacement"), 0o600); err != nil {
			return err
		}
		var statErr error
		original, statErr = os.Stat(walPath)
		if statErr != nil {
			return statErr
		}
		return sentinel
	}
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(testStageEntity("Q1", "Alpha", 1)); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	err = stage.finalizeScratch(context.Background(), []byte("{}"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("finalize error = %v, want %v", err, sentinel)
	}
	if stage.commitAmbiguous || !stage.unownedDuckFiles {
		t.Fatalf("ownership state ambiguous=%t unowned=%t", stage.commitAmbiguous, stage.unownedDuckFiles)
	}
	got, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("replacement WAL lost: %v", statErr)
	}
	body, readErr := os.ReadFile(walPath)
	if readErr != nil || string(body) != "replacement" || !os.SameFile(got, original) {
		t.Fatalf("replacement WAL = %q, err %v", body, readErr)
	}
	if _, statErr := os.Lstat(stage.workspace.privatePath); statErr != nil {
		t.Fatalf("private container missing: %v", statErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func TestWikidataDumpStagingSinkFlushFailureAttemptsAllAppendersAndDoesNotCommit(t *testing.T) {
	finalOut := filepath.Join(t.TempDir(), "model")
	stage, err := NewWikidataDumpStagingSink(context.Background(), finalOut)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	t.Cleanup(func() { _ = stage.Abort() })
	stage.transactionLimit = 1
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	value := 1.0
	entity := testStageEntity("Q1", "Alpha", 1)
	entity.Rel = []model.SeedRel{{Type: "part_of", Target: "Q1"}}
	entity.Props = []model.SeedProp{{Property: "count", Value: &value, ValueType: "measured", Source: "test"}}
	if err := stage.AcceptEntity(entity); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	if err := stage.Reject(ingest.Reject{Source: ingest.RejectSourceWikidataDump, File: "dump", Line: 1, Reason: "bad"}); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	sentinel := errors.New("commit must not be attempted")
	commitCalled := false
	watchedTx := &stagedCommitErrorTx{
		Tx: stage.tx, delegate: true,
		afterDelegate: func() { commitCalled = true },
		err:           sentinel,
	}
	stage.tx = watchedTx
	var hookErr error
	stage.rotationHook = func(boundary string) error {
		if boundary != "before-flush" {
			return nil
		}
		_, hookErr = driverExec(stage.ctx, stage.conn, `INSERT INTO seen_qid VALUES (?)`, "Q1")
		return nil
	}
	err = stage.MarkSeenQID("Q2")
	if hookErr == nil || (!strings.Contains(strings.ToLower(hookErr.Error()), "duplicate") && !strings.Contains(strings.ToLower(hookErr.Error()), "constraint")) {
		t.Fatalf("hook error = %v, want duplicate/constraint failure", hookErr)
	}
	if err == nil {
		t.Fatal("rotation succeeded after the transaction was aborted")
	}
	for _, appender := range []string{"entity", "category", "relationship", "claim", "reject"} {
		if !strings.Contains(err.Error(), "flush "+appender+" appender") {
			t.Fatalf("rotation error = %v, missing %s flush", err, appender)
		}
	}
	if stage.tx != watchedTx {
		t.Fatalf("transaction detached after flush failure: %T", stage.tx)
	}
	if commitCalled || errors.Is(err, sentinel) {
		t.Fatalf("commit called or sentinel returned: called=%t err=%v", commitCalled, err)
	}
	if abortErr := stage.Abort(); abortErr != nil && !strings.Contains(abortErr.Error(), "Current transaction is aborted") {
		t.Fatalf("Abort: %v", abortErr)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, statErr := os.Lstat(stage.workspace.privatePath); !os.IsNotExist(statErr) {
		t.Fatalf("private stage remains: %v", statErr)
	}
	if _, statErr := os.Lstat(finalOut); !os.IsNotExist(statErr) {
		t.Fatalf("final output exists: %v", statErr)
	}
}

func testStageEntity(seedID, name string, importance float64) *model.Entity {
	return &model.Entity{
		SeedID: seedID, Name: name, Type: "event", T0: model.YearToSeconds(1900), T1: model.YearToSeconds(1900),
		Precision: "year", Status: "documented", Importance: importance, Categories: []string{"science"},
	}
}

func TestWikidataDumpStagingSinkCleansUpAfterCanceledMarkSeenQID(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	stage, err := NewWikidataDumpStagingSink(ctx, filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	cancel()
	if err := stage.MarkSeenQID("Q1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled MarkSeenQID error = %v, want context.Canceled", err)
	}
	if err := stage.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	assertStageDataHandlesClosed(t, stage)
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains after abort: %v", err)
	}
}

func TestWikidataDumpStagingSinkRejectsEmptySeedID(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	t.Cleanup(func() { _ = stage.Abort() })
	entity := &model.Entity{Name: "No ID", Type: "event", Precision: "year", Status: "documented", Importance: 1}
	if err := stage.AcceptEntity(entity); err == nil || !strings.Contains(err.Error(), "empty seed id") {
		t.Fatalf("AcceptEntity error = %v", err)
	}
}

func TestWikidataDumpStagingSinkRejectsMissingRelationshipTarget(t *testing.T) {
	t.Parallel()

	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	privatePath := stage.workspace.privatePath
	entity := &model.Entity{
		SeedID: "source", Name: "Source", Type: "event", Precision: "year", Status: "documented",
		Categories: []string{"science"}, Importance: 1,
		Rel: []model.SeedRel{{Type: "part_of", Target: "missing"}},
	}
	if err := stage.MarkSeenQID("Q1"); err != nil {
		t.Fatalf("MarkSeenQID: %v", err)
	}
	if err := stage.AcceptEntity(entity); err != nil {
		t.Fatalf("AcceptEntity: %v", err)
	}
	if err := stage.finalizeScratch(context.Background(), []byte("{}")); err == nil ||
		!strings.Contains(err.Error(), "missing relationship target") {
		t.Fatalf("finalizeScratch error = %v", err)
	}
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private stage remains after reference failure: %v", err)
	}
}

func TestWikidataDumpStagingSinkSortsRejectsLikeLegacyWriter(t *testing.T) {
	t.Parallel()

	rows := []RejectRow{
		{Source: "wikidata-dump", File: "wikidata", Line: 9, Reason: "z"},
		{Source: "wikidata-dump", File: "wikidata", Line: 2, Reason: "a"},
		{Source: "other", File: "b", Line: 3, Reason: "m"},
	}
	legacy, err := WriteRejects(context.Background(), t.TempDir(), rows)
	if err != nil {
		t.Fatalf("WriteRejects: %v", err)
	}
	stage, err := NewWikidataDumpStagingSink(context.Background(), filepath.Join(t.TempDir(), "model"))
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	t.Cleanup(func() {
		if err := stage.Abort(); err != nil {
			t.Errorf("Abort: %v", err)
		}
	})
	for _, row := range rows {
		if err := stage.Reject(ingest.Reject{Source: ingest.RejectSource(row.Source), File: row.File, Line: row.Line, Reason: row.Reason}); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	}
	if err := stage.finalizeScratch(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("finalizeScratch: %v", err)
	}
	if stage.rejectRows != len(rows) {
		t.Fatalf("reject rows = %d, want %d", stage.rejectRows, len(rows))
	}
	assertSameFileBytes(t, filepath.Join(stage.workspace.exportsPath(), "reject.parquet"), legacy.Path)
}

func assertSameFileBytes(t *testing.T, gotPath, wantPath string) {
	t.Helper()
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read %s: %v", gotPath, err)
	}
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s differs from %s: sha256 %x vs %x", gotPath, wantPath, sha256.Sum256(got), sha256.Sum256(want))
	}
}

func rejectRowsForStageTest(rejects []ingest.Reject) []RejectRow {
	rows := make([]RejectRow, 0, len(rejects))
	for _, reject := range rejects {
		rows = append(rows, RejectRow{Source: string(reject.Source), File: reject.File, Line: reject.Line, Reason: reject.Reason})
	}
	return rows
}

func duplicateStageInput(withTime bool) string {
	timeClaim := ""
	if withTime {
		timeClaim = `,"P585":[{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001900-01-01T00:00:00Z","timezone":0,"before":0,"after":0,"precision":9,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}]`
	}
	item := `{"id":"Q9401","type":"item","labels":{"en":{"value":"Duplicate"}},"claims":{"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}]%s}}`
	return fmt.Sprintf("["+item+","+item+"]", timeClaim, timeClaim)
}

func cloneStageEntities(entities []*model.Entity) []*model.Entity {
	clones := make([]*model.Entity, len(entities))
	for i, entity := range entities {
		clone := *entity
		clone.Categories = append([]string(nil), entity.Categories...)
		clones[i] = &clone
	}
	return clones
}

func assertStageDataHandlesClosed(t *testing.T, stage *WikidataDumpStagingSink) {
	t.Helper()
	if stage.tx != nil || stage.conn != nil || stage.connector != nil || stage.seenQIDStmt != nil ||
		stage.entityAppender != nil || stage.categoryAppender != nil ||
		stage.relationshipAppender != nil || stage.claimAppender != nil || stage.rejectAppender != nil {
		t.Fatal("staging sink returned with a DuckDB transaction, connection, connector, statement, or appender still held")
	}
}

func ensureStageWALForTest(t *testing.T, stage *WikidataDumpStagingSink) {
	t.Helper()
	if _, err := stage.workspace.duckRoot.Lstat("model.duckdb.wal"); errors.Is(err, os.ErrNotExist) {
		if err := stage.workspace.duckRoot.WriteFile("model.duckdb.wal", []byte("wal"), 0o600); err != nil {
			t.Fatalf("create staged WAL: %v", err)
		}
	} else if err != nil {
		t.Fatalf("stat staged WAL: %v", err)
	}
	if err := stage.workspace.recordDuckFiles(); err != nil {
		t.Fatalf("record staged WAL: %v", err)
	}
}

func stageBoundaryPath(workspace *stagingWorkspace, key string) string {
	if key == stagePrivateDirectory {
		return workspace.privatePath
	}
	return filepath.Join(workspace.privatePath, key)
}

func stagedModelFiles(stage *WikidataDumpStagingSink) []ModelFile {
	rows := map[string]int{
		"entity": stage.entityRows, "entity_category": stage.categoryRows,
		"relationship": stage.relationshipRows, "claim": stage.claimRows,
	}
	files := make([]ModelFile, 0, len(fileDefinitions)+1)
	for _, definition := range fileDefinitions {
		files = append(files, ModelFile{
			Name: definition.name, Path: filepath.Join(stage.workspace.exportsPath(), definition.name),
			Rows: rows[definition.table],
		})
	}
	return append(files, ModelFile{
		Name: "reject.parquet", Path: filepath.Join(stage.workspace.exportsPath(), "reject.parquet"), Rows: stage.rejectRows,
	})
}

func finalizedModelFiles(dir string, stage *WikidataDumpStagingSink) []ModelFile {
	rows := map[string]int{
		"entity": stage.entityRows, "entity_category": stage.categoryRows,
		"relationship": stage.relationshipRows, "claim": stage.claimRows,
	}
	files := make([]ModelFile, 0, len(fileDefinitions)+1)
	for _, definition := range fileDefinitions {
		files = append(files, ModelFile{Name: definition.name, Path: filepath.Join(dir, definition.name), Rows: rows[definition.table]})
	}
	return append(files, ModelFile{Name: "reject.parquet", Path: filepath.Join(dir, "reject.parquet"), Rows: stage.rejectRows})
}

func assertFinalizedManifest(t *testing.T, dir string, files []ModelFile, report []byte) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, modelFilesManifestName))
	if err != nil {
		t.Fatalf("read model file manifest: %v", err)
	}
	var manifest modelFilesManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode model file manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Files) != len(files) {
		t.Fatalf("manifest header = %#v", manifest)
	}
	reportHash := sha256.Sum256(report)
	if manifest.Import.Name != importReportFileName || manifest.Import.SHA256 != hex.EncodeToString(reportHash[:]) || manifest.Import.Size != int64(len(report)) {
		t.Fatalf("manifest import = %#v", manifest.Import)
	}
	for i, file := range files {
		body, err := os.ReadFile(file.Path)
		if err != nil {
			t.Fatalf("read finalized %s: %v", file.Name, err)
		}
		hash := sha256.Sum256(body)
		got := manifest.Files[i]
		if got.Name != file.Name || got.SHA256 != hex.EncodeToString(hash[:]) || got.Size != int64(len(body)) || got.Rows != int64(file.Rows) {
			t.Fatalf("manifest file %d = %#v", i, got)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 7 {
		t.Fatalf("finalized artifacts = %v, err %v", entries, err)
	}
}

func assertWorkspaceHandlesClosed(t *testing.T, workspace *stagingWorkspace) {
	t.Helper()
	if workspace.publicParent != nil || workspace.publicParentRoot != nil || workspace.privateDir != nil || workspace.privateRoot != nil ||
		workspace.scratchDir != nil || workspace.scratchRoot != nil || workspace.duckDir != nil || workspace.duckRoot != nil ||
		workspace.exportsDir != nil || workspace.exportsRoot != nil || workspace.readyDir != nil || workspace.readyRoot != nil {
		t.Fatalf("finalized workspace retains handles: %#v", workspace)
	}
}
