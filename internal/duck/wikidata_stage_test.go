package duck

import (
	"context"
	"crypto/sha256"
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
