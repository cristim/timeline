package duck

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

type stagingWorkspace struct {
	outputName string
	publicPath string

	publicParent     *os.File
	publicParentRoot *os.Root
	publicParentInfo os.FileInfo

	privateName string
	privatePath string
	privateDir  *os.File
	privateRoot *os.Root
	privateInfo os.FileInfo

	scratchRoot *os.Root
	scratchInfo os.FileInfo
	duckRoot    *os.Root
	duckInfo    os.FileInfo
	exportsRoot *os.Root
	exportsInfo os.FileInfo
	readyRoot   *os.Root
	readyInfo   os.FileInfo

	fileIdentities map[string]os.FileInfo

	beforeRemove func(string)
	closed       bool
}

const (
	stageEntityFile       = "scratch/exports/entity.parquet"
	stageCategoryFile     = "scratch/exports/entity_category.parquet"
	stageRelationshipFile = "scratch/exports/relationship.parquet"
	stageClaimFile        = "scratch/exports/claim.parquet"
	stageRejectFile       = "scratch/exports/reject.parquet"
	stageImportFile       = "scratch/exports/import.json"
	stageDatabaseFile     = "scratch/duck/model.duckdb"
	stageDatabaseWALFile  = "scratch/duck/model.duckdb.wal"
	stageExportsDirectory = "scratch/exports"
	stageDuckDirectory    = "scratch/duck"
	stageScratchDirectory = "scratch"
	stageReadyDirectory   = "ready"
	stagePrivateDirectory = "."
)

func newStagingWorkspace(outputPath string) (_ *stagingWorkspace, err error) {
	absOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return nil, fmt.Errorf("resolve model output: %w", err)
	}
	outputName := filepath.Base(absOutput)
	if outputName == "." || outputName == string(filepath.Separator) || outputName == "" {
		return nil, fmt.Errorf("model output must name an absent child directory")
	}
	publicPath := filepath.Dir(absOutput)

	w := &stagingWorkspace{
		outputName: outputName, publicPath: publicPath,
		fileIdentities: make(map[string]os.FileInfo),
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.cleanup())
		}
	}()

	w.publicParent, err = os.Open(publicPath)
	if err != nil {
		return nil, fmt.Errorf("open model output parent: %w", err)
	}
	w.publicParentInfo, err = w.publicParent.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat model output parent: %w", err)
	}
	if !w.publicParentInfo.IsDir() {
		return nil, fmt.Errorf("model output parent is not a directory")
	}
	w.publicParentRoot, err = os.OpenRoot(publicPath)
	if err != nil {
		return nil, fmt.Errorf("open rooted model output parent: %w", err)
	}
	rootInfo, err := w.publicParentRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("stat rooted model output parent: %w", err)
	}
	if !os.SameFile(w.publicParentInfo, rootInfo) {
		return nil, fmt.Errorf("model output parent changed while opening")
	}
	if _, err := w.publicParentRoot.Lstat(outputName); err == nil {
		return nil, fmt.Errorf("model output already exists: %s", absOutput)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check model output: %w", err)
	}

	w.privateName, w.privateDir, w.privateRoot, w.privateInfo, err = createPrivateStageDirectory(w.publicParentRoot)
	if err != nil {
		return nil, fmt.Errorf("create private model stage: %w", err)
	}
	w.privatePath = filepath.Join(publicPath, w.privateName)
	if w.privateInfo.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("private model stage mode is %04o, want 0700", w.privateInfo.Mode().Perm())
	}
	if err := w.verifyPrivateIdentity(); err != nil {
		return nil, err
	}

	if w.scratchRoot, w.scratchInfo, err = createHeldDirectory(w.privateRoot, "scratch"); err != nil {
		return nil, fmt.Errorf("create private scratch: %w", err)
	}
	if w.duckRoot, w.duckInfo, err = createHeldDirectory(w.scratchRoot, "duck"); err != nil {
		return nil, fmt.Errorf("create private DuckDB directory: %w", err)
	}
	if w.exportsRoot, w.exportsInfo, err = createHeldDirectory(w.scratchRoot, "exports"); err != nil {
		return nil, fmt.Errorf("create private exports directory: %w", err)
	}
	if w.readyRoot, w.readyInfo, err = createHeldDirectory(w.privateRoot, "ready"); err != nil {
		return nil, fmt.Errorf("create private ready directory: %w", err)
	}
	return w, nil
}

func createPrivateStageDirectory(parent *os.Root) (name string, dir *os.File, root *os.Root, info os.FileInfo, err error) {
	return createPrivateStageDirectoryWithHook(parent, nil)
}

func createPrivateStageDirectoryWithHook(parent *os.Root, beforeFirstOpen func(string)) (name string, dir *os.File, root *os.Root, info os.FileInfo, err error) {
	for range 100 {
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			return "", nil, nil, nil, fmt.Errorf("generate private stage name: %w", err)
		}
		name = ".wk-model-stage-" + hex.EncodeToString(token[:])
		if err = parent.Mkdir(name, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return name, nil, nil, nil, fmt.Errorf("mkdir private stage %q: %w", name, err)
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return name, nil, nil, nil, fmt.Errorf("lstat newly created private stage %q: %w", name, err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			return name, nil, nil, nil, fmt.Errorf("newly created private stage %q has mode %v, want directory mode 0700", name, info.Mode())
		}
		if err := validateStageOwner(info); err != nil {
			return name, nil, nil, nil, fmt.Errorf("validate newly created private stage %q owner: %w", name, err)
		}
		defer func() {
			if err != nil {
				err = errors.Join(err, closeFile("close unverified private stage", dir), closeRoot("close unverified private stage root", root))
				dir = nil
				root = nil
				info = nil
			}
		}()
		if beforeFirstOpen != nil {
			beforeFirstOpen(name)
		}
		dir, err = parent.Open(name)
		if err != nil {
			return name, nil, nil, nil, fmt.Errorf("open newly created private stage %q: %w", name, err)
		}
		dirInfo, err := dir.Stat()
		if err != nil {
			return name, dir, nil, nil, fmt.Errorf("stat newly created private stage handle %q: %w", name, err)
		}
		root, err = parent.OpenRoot(name)
		if err != nil {
			return name, dir, nil, nil, fmt.Errorf("open newly created private stage root %q: %w", name, err)
		}
		rootInfo, err := root.Stat(".")
		if err != nil {
			return name, dir, root, nil, fmt.Errorf("stat newly created private stage root %q: %w", name, err)
		}
		namedInfo, err := parent.Lstat(name)
		if err != nil {
			return name, dir, root, nil, fmt.Errorf("re-lstat newly created private stage %q: %w", name, err)
		}
		if !os.SameFile(info, dirInfo) || !os.SameFile(info, rootInfo) || !os.SameFile(info, namedInfo) {
			return name, dir, root, info, fmt.Errorf("newly created private stage %q identity changed during allocation", name)
		}
		return name, dir, root, info, nil
	}
	return "", nil, nil, nil, fmt.Errorf("could not allocate a unique private stage name")
}

func createHeldDirectory(parent *os.Root, name string) (_ *os.Root, _ os.FileInfo, err error) {
	if err := parent.Mkdir(name, 0o700); err != nil {
		return nil, nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeRoot("close unverified directory root", root))
		}
	}()
	rootInfo, err := root.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !pathInfo.IsDir() || !os.SameFile(pathInfo, rootInfo) {
		return nil, nil, fmt.Errorf("directory %s identity changed while opening", name)
	}
	return root, pathInfo, nil
}

func (w *stagingWorkspace) duckDBPath() string {
	return filepath.Join(w.privatePath, "scratch", "duck", "model.duckdb")
}

func (w *stagingWorkspace) exportsPath() string {
	return filepath.Join(w.privatePath, "scratch", "exports")
}

func (w *stagingWorkspace) verifyPrivateIdentity() error {
	if w.privateInfo == nil {
		return nil
	}
	if w.publicParentRoot == nil {
		return fmt.Errorf("private model stage has no held public parent")
	}
	pathInfo, err := w.publicParentRoot.Lstat(w.privateName)
	if err != nil {
		return fmt.Errorf("stat named private model stage: %w", err)
	}
	if !os.SameFile(w.privateInfo, pathInfo) {
		return fmt.Errorf("private model stage identity changed")
	}
	if w.privateRoot != nil {
		rootInfo, err := w.privateRoot.Stat(".")
		if err != nil {
			return fmt.Errorf("stat held private model stage: %w", err)
		}
		if !os.SameFile(w.privateInfo, rootInfo) {
			return fmt.Errorf("held private model stage identity changed")
		}
	}
	return nil
}

func (w *stagingWorkspace) verifyScratchPathIdentity() error {
	pathParentInfo, err := os.Stat(w.publicPath)
	if err != nil {
		return fmt.Errorf("stat named model output parent: %w", err)
	}
	if !os.SameFile(w.publicParentInfo, pathParentInfo) {
		return fmt.Errorf("model output parent identity changed")
	}
	if err := w.verifyPrivateIdentity(); err != nil {
		return err
	}
	if err := verifyHeldDirectory(w.privateRoot, "scratch", w.scratchRoot, w.scratchInfo); err != nil {
		return err
	}
	if err := verifyHeldDirectory(w.scratchRoot, "duck", w.duckRoot, w.duckInfo); err != nil {
		return err
	}
	if err := verifyHeldDirectory(w.scratchRoot, "exports", w.exportsRoot, w.exportsInfo); err != nil {
		return err
	}
	return nil
}

func verifyHeldDirectory(parent *os.Root, name string, held *os.Root, want os.FileInfo) error {
	if parent == nil || held == nil || want == nil {
		return fmt.Errorf("private stage directory %s has no held identity", name)
	}
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat private stage directory %s: %w", name, err)
	}
	heldInfo, err := held.Stat(".")
	if err != nil {
		return fmt.Errorf("stat held private stage directory %s: %w", name, err)
	}
	if !os.SameFile(pathInfo, want) || !os.SameFile(heldInfo, want) {
		return fmt.Errorf("private stage directory %s identity changed", name)
	}
	return nil
}

func (w *stagingWorkspace) recordExportFile(name string, file *os.File) error {
	return w.recordFile(w.exportsRoot, name, path.Join(stageExportsDirectory, name), file)
}

func (w *stagingWorkspace) recordDuckFiles() error {
	var errs []error
	for _, file := range []struct {
		name string
		key  string
	}{
		{name: "model.duckdb", key: stageDatabaseFile},
		{name: "model.duckdb.wal", key: stageDatabaseWALFile},
	} {
		if err := w.recordFile(w.duckRoot, file.name, file.key, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *stagingWorkspace) recordFile(root *os.Root, name, key string, file *os.File) error {
	if root == nil {
		return fmt.Errorf("record private stage file %s: containing root is closed", key)
	}
	pathInfo, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("record private stage file %s: %w", key, err)
	}
	if !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("private stage file %s is not regular", key)
	}
	if file != nil {
		fileInfo, err := file.Stat()
		if err != nil {
			return fmt.Errorf("stat held private stage file %s: %w", key, err)
		}
		if !os.SameFile(pathInfo, fileInfo) {
			return fmt.Errorf("private stage file %s identity changed while opening", key)
		}
	}
	if recorded := w.fileIdentities[key]; recorded != nil && !os.SameFile(recorded, pathInfo) {
		return fmt.Errorf("private stage file %s identity changed", key)
	}
	w.fileIdentities[key] = pathInfo
	return nil
}

func (w *stagingWorkspace) verifyExportsIdentity() error {
	if err := w.verifyPrivateIdentity(); err != nil {
		return err
	}
	if err := verifyHeldDirectory(w.privateRoot, "scratch", w.scratchRoot, w.scratchInfo); err != nil {
		return err
	}
	return verifyHeldDirectory(w.scratchRoot, "exports", w.exportsRoot, w.exportsInfo)
}

func (w *stagingWorkspace) verifyDuckIdentity() error {
	if err := w.verifyPrivateIdentity(); err != nil {
		return err
	}
	if err := verifyHeldDirectory(w.privateRoot, "scratch", w.scratchRoot, w.scratchInfo); err != nil {
		return err
	}
	return verifyHeldDirectory(w.scratchRoot, "duck", w.duckRoot, w.duckInfo)
}

func (w *stagingWorkspace) cleanup() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	canClean := true
	if w.privateInfo != nil {
		if err := w.verifyPrivateIdentity(); err != nil {
			errs = append(errs, err)
			canClean = false
		}
	}
	if canClean && w.privateRoot != nil {
		if err := w.verifyPrivateIdentity(); err != nil {
			errs = append(errs, err)
		} else if err := verifyHeldDirectory(w.privateRoot, "scratch", w.scratchRoot, w.scratchInfo); err != nil {
			errs = append(errs, err)
		} else {
			if err := w.verifyExportsIdentity(); err != nil {
				errs = append(errs, err)
			} else {
				for _, file := range []struct {
					name string
					key  string
				}{
					{name: "entity.parquet", key: stageEntityFile},
					{name: "entity_category.parquet", key: stageCategoryFile},
					{name: "relationship.parquet", key: stageRelationshipFile},
					{name: "claim.parquet", key: stageClaimFile},
					{name: "reject.parquet", key: stageRejectFile},
					{name: "import.json", key: stageImportFile},
				} {
					w.removeRecordedFile(w.exportsRoot, file.name, file.key, w.verifyExportsIdentity, &errs)
				}
			}
			if err := w.verifyDuckIdentity(); err != nil {
				errs = append(errs, err)
			} else {
				w.removeRecordedFile(w.duckRoot, "model.duckdb.wal", stageDatabaseWALFile, w.verifyDuckIdentity, &errs)
				w.removeRecordedFile(w.duckRoot, "model.duckdb", stageDatabaseFile, w.verifyDuckIdentity, &errs)
			}
		}
	}
	errs = appendCloseError(errs, "close private exports root", closeRootValue(&w.exportsRoot))
	errs = appendCloseError(errs, "close private DuckDB root", closeRootValue(&w.duckRoot))
	if canClean && w.scratchRoot != nil {
		verifyScratch := func() error {
			if err := w.verifyPrivateIdentity(); err != nil {
				return err
			}
			return verifyHeldDirectory(w.privateRoot, "scratch", w.scratchRoot, w.scratchInfo)
		}
		w.removeRecordedDirectory(w.scratchRoot, "exports", stageExportsDirectory, w.exportsInfo, verifyScratch, &errs)
		w.removeRecordedDirectory(w.scratchRoot, "duck", stageDuckDirectory, w.duckInfo, verifyScratch, &errs)
	}
	errs = appendCloseError(errs, "close private scratch root", closeRootValue(&w.scratchRoot))
	errs = appendCloseError(errs, "close private ready root", closeRootValue(&w.readyRoot))
	if canClean && w.privateRoot != nil {
		w.removeRecordedDirectory(w.privateRoot, "scratch", stageScratchDirectory, w.scratchInfo, w.verifyPrivateIdentity, &errs)
		w.removeRecordedDirectory(w.privateRoot, "ready", stageReadyDirectory, w.readyInfo, w.verifyPrivateIdentity, &errs)
	}
	if w.privateRoot != nil {
		errs = appendCloseError(errs, "close private model root", w.privateRoot.Close())
		w.privateRoot = nil
	}
	if w.privateDir != nil {
		errs = appendCloseError(errs, "close private model directory", w.privateDir.Close())
		w.privateDir = nil
	}
	if canClean && w.publicParentRoot != nil && w.privateInfo != nil {
		w.runBeforeRemove(stagePrivateDirectory)
		if err := w.verifyPrivateIdentity(); err != nil {
			errs = append(errs, err)
		} else if err := w.publicParentRoot.Remove(w.privateName); err != nil {
			errs = append(errs, fmt.Errorf("remove private model stage: %w", err))
		}
	}
	if w.publicParentRoot != nil {
		errs = appendCloseError(errs, "close model output parent root", w.publicParentRoot.Close())
		w.publicParentRoot = nil
	}
	if w.publicParent != nil {
		errs = appendCloseError(errs, "close model output parent", w.publicParent.Close())
		w.publicParent = nil
	}
	return errors.Join(errs...)
}

func (w *stagingWorkspace) removeRecordedFile(root *os.Root, name, key string, verifyContainer func() error, errs *[]error) {
	want := w.fileIdentities[key]
	if want == nil {
		if _, err := root.Lstat(name); err == nil {
			*errs = append(*errs, fmt.Errorf("private stage file %s has no recorded identity", key))
		} else if !errors.Is(err, os.ErrNotExist) {
			*errs = append(*errs, fmt.Errorf("stat private stage file %s: %w", key, err))
		}
		return
	}
	if err := verifyContainer(); err != nil {
		*errs = append(*errs, err)
		return
	}
	w.runBeforeRemove(key)
	if err := verifyContainer(); err != nil {
		*errs = append(*errs, err)
		return
	}
	got, err := root.Lstat(name)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("stat private stage file %s: %w", key, err))
		return
	}
	if !os.SameFile(got, want) {
		*errs = append(*errs, fmt.Errorf("private stage file %s identity changed", key))
		return
	}
	if err := root.Remove(name); err != nil {
		*errs = append(*errs, fmt.Errorf("remove private stage file %s: %w", key, err))
	}
}

func (w *stagingWorkspace) removeRecordedDirectory(parent *os.Root, name, key string, want os.FileInfo, verifyParent func() error, errs *[]error) {
	if want == nil {
		*errs = append(*errs, fmt.Errorf("private stage directory %s has no recorded identity", key))
		return
	}
	if err := verifyParent(); err != nil {
		*errs = append(*errs, err)
		return
	}
	w.runBeforeRemove(key)
	if err := verifyParent(); err != nil {
		*errs = append(*errs, err)
		return
	}
	got, err := parent.Lstat(name)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("stat private stage directory %s: %w", key, err))
		return
	}
	if !os.SameFile(got, want) {
		*errs = append(*errs, fmt.Errorf("private stage directory %s identity changed", key))
		return
	}
	if err := parent.Remove(name); err != nil {
		*errs = append(*errs, fmt.Errorf("remove private stage directory %s: %w", key, err))
	}
}

func (w *stagingWorkspace) runBeforeRemove(name string) {
	if w.beforeRemove != nil {
		w.beforeRemove(name)
	}
}

func closeRootValue(root **os.Root) error {
	if *root == nil {
		return nil
	}
	err := (*root).Close()
	*root = nil
	return err
}

func closeRoot(operation string, root *os.Root) error {
	if root == nil {
		return nil
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func closeFile(operation string, file *os.File) error {
	if file == nil {
		return nil
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func appendCloseError(errs []error, operation string, err error) []error {
	if err != nil {
		return append(errs, fmt.Errorf("%s: %w", operation, err))
	}
	return errs
}
