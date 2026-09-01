package duck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const modelFilesManifestName = "model-files.json"

type fileContentIdentity struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type committedFileIdentity struct {
	fileContentIdentity
	Rows int64 `json:"rows"`
}

type modelFilesManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Import        fileContentIdentity     `json:"import"`
	Files         []committedFileIdentity `json:"files"`
}

func (s *WikidataDumpStagingSink) Finalize(ctx context.Context, importReport []byte) (err error) {
	if err := s.finalizeScratch(ctx, importReport); err != nil {
		return err
	}
	defer func() {
		if err != nil && !s.committed {
			err = errors.Join(err, s.Abort())
		}
	}()

	manifest := modelFilesManifest{SchemaVersion: 1, Files: make([]committedFileIdentity, 0, 5)}
	rows := map[string]int64{
		"entity.parquet": int64(s.entityRows), "entity_category.parquet": int64(s.categoryRows),
		"relationship.parquet": int64(s.relationshipRows), "claim.parquet": int64(s.claimRows),
		"reject.parquet": int64(s.rejectRows),
	}
	for _, name := range []string{"entity.parquet", "entity_category.parquet", "relationship.parquet", "claim.parquet", "reject.parquet"} {
		identity, copyErr := s.copyScratchToReady(ctx, name, rows[name])
		if copyErr != nil {
			return copyErr
		}
		manifest.Files = append(manifest.Files, identity)
	}
	importIdentity, err := s.copyScratchToReady(ctx, importReportFileName, 0)
	if err != nil {
		return err
	}
	manifest.Import = importIdentity.fileContentIdentity
	if err := contextBoundary(ctx, "before model file manifest"); err != nil {
		return err
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal model file manifest: %w", err)
	}
	if err := s.writeReadyFile(modelFilesManifestName, body); err != nil {
		return err
	}
	if err := contextBoundary(ctx, "after model file manifest"); err != nil {
		return err
	}
	if err := contextBoundary(ctx, "before scratch teardown"); err != nil {
		return err
	}
	if err := s.runFinalizeBoundary("teardown-scratch"); err != nil {
		return err
	}
	if err := s.workspace.teardownScratch(); err != nil {
		return err
	}
	if err := s.workspace.verifyReadyIdentity(); err != nil {
		return err
	}
	if err := contextBoundary(ctx, "before ready directory sync"); err != nil {
		return err
	}
	if err := s.runFinalizeBoundary("sync-ready-directory"); err != nil {
		return err
	}
	if err := s.workspace.readyDir.Sync(); err != nil {
		return fmt.Errorf("sync ready model directory: %w", err)
	}
	if s.beforeInstall != nil {
		s.beforeInstall()
	}
	if err := s.workspace.verifyReadyIdentity(); err != nil {
		return err
	}
	if err := contextBoundary(ctx, "before model install"); err != nil {
		return err
	}
	if err := s.runFinalizeBoundary("install-directory"); err != nil {
		return err
	}
	if err := contextBoundary(ctx, "immediately before model install"); err != nil {
		return err
	}
	if err := installDirectoryNoReplace(int(s.workspace.privateDir.Fd()), "ready", s.workspace.readyInfo, int(s.workspace.publicParent.Fd()), s.workspace.outputName, s.insideInstall); err != nil {
		return fmt.Errorf("install completed model: %w", err)
	}
	s.committed = true
	return s.workspace.finishCommitted(s.finalizeHook)
}

func contextBoundary(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func (s *WikidataDumpStagingSink) copyScratchToReady(ctx context.Context, name string, rows int64) (_ committedFileIdentity, err error) {
	if err := s.workspace.verifyExportsIdentity(); err != nil {
		return committedFileIdentity{}, err
	}
	source, err := openNoFollowAt(s.workspace.exportsDir, name)
	if err != nil {
		return committedFileIdentity{}, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return committedFileIdentity{}, fmt.Errorf("stat scratch artifact %s: regular=%t: %w", name, info != nil && info.Mode().IsRegular(), err)
	}
	key := "scratch/exports/" + name
	if want := s.workspace.fileIdentities[key]; want == nil || !os.SameFile(want, info) {
		return committedFileIdentity{}, fmt.Errorf("scratch artifact %s identity changed", name)
	}
	destination, err := s.workspace.readyRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return committedFileIdentity{}, fmt.Errorf("create ready artifact %s: %w", name, err)
	}
	closeDestination := true
	defer func() {
		if closeDestination {
			err = errors.Join(err, destination.Close())
		}
	}()
	if err := s.workspace.recordReadyFile(name, destination); err != nil {
		return committedFileIdentity{}, err
	}
	if err := s.runFinalizeBoundary("copy-ready:" + name); err != nil {
		return committedFileIdentity{}, err
	}
	hash := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(destination, hash), source)
	if err != nil {
		return committedFileIdentity{}, fmt.Errorf("copy ready artifact %s: %w", name, err)
	}
	if err := s.runFinalizeBoundary("sync-ready:" + name); err != nil {
		return committedFileIdentity{}, err
	}
	if err := destination.Sync(); err != nil {
		return committedFileIdentity{}, fmt.Errorf("sync ready artifact %s: %w", name, err)
	}
	if err := s.workspace.recordReadyFile(name, destination); err != nil {
		return committedFileIdentity{}, err
	}
	if err := destination.Close(); err != nil {
		return committedFileIdentity{}, fmt.Errorf("close ready artifact %s: %w", name, err)
	}
	closeDestination = false
	return committedFileIdentity{fileContentIdentity: fileContentIdentity{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, Rows: rows}, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func (s *WikidataDumpStagingSink) writeReadyFile(name string, body []byte) (err error) {
	file, err := s.workspace.readyRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create ready %s: %w", name, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := s.workspace.recordReadyFile(name, file); err != nil {
		return err
	}
	if err := s.runFinalizeBoundary("write-ready:" + name); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write ready %s: %w", name, err)
	}
	if err := s.runFinalizeBoundary("sync-ready:" + name); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync ready %s: %w", name, err)
	}
	return s.workspace.recordReadyFile(name, file)
}
