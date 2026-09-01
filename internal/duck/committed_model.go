package duck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"wk/internal/ingest"
	"wk/internal/model"
)

var committedModelNames = []string{
	modelFilesManifestName, importReportFileName, "entity.parquet", "entity_category.parquet",
	"relationship.parquet", "claim.parquet", "reject.parquet",
}

const (
	maxCommittedMetadataBytes = 16 << 20
	maxCommittedJSONDepth     = 64
)

type heldModelFile struct {
	file *os.File
	info os.FileInfo
	hash string
	size int64
}

type CommittedModel struct {
	parent  *os.File
	path    string
	info    os.FileInfo
	root    *os.Root
	files   map[string]*heldModelFile
	version string
	closed  bool
}

func OpenCommittedModel(ctx context.Context, dir string) (_ *CommittedModel, err error) {
	return openCommittedModel(ctx, dir, nil)
}

func openCommittedModel(ctx context.Context, dir string, beforeFinalVerify func()) (_ *CommittedModel, err error) {
	parent, err := openDirectoryNoFollow(dir)
	if err != nil {
		return nil, fmt.Errorf("open committed model: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open rooted committed model: %w", err), parent.Close())
	}
	parentInfo, err := parent.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat committed model: %w", err), root.Close(), parent.Close())
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat rooted committed model: %w", err), root.Close(), parent.Close())
	}
	pathInfo, err := os.Lstat(dir)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, rootInfo) || !os.SameFile(parentInfo, pathInfo) {
		return nil, errors.Join(fmt.Errorf("committed model directory identity changed: lstat=%v", err), root.Close(), parent.Close())
	}
	m := &CommittedModel{parent: parent, root: root, path: dir, info: parentInfo, files: make(map[string]*heldModelFile, len(committedModelNames))}
	defer func() {
		if err != nil {
			err = errors.Join(err, m.Close())
		}
	}()
	if err := verifyCommittedEntries(ctx, root); err != nil {
		return nil, err
	}
	for _, name := range committedModelNames {
		file, err := openNoFollowAt(parent, name)
		if err != nil {
			return nil, err
		}
		info, statErr := file.Stat()
		pathInfo, pathErr := root.Lstat(name)
		if statErr != nil || pathErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
			return nil, errors.Join(
				fmt.Errorf("committed model file %s is not a stable regular file: stat=%v path=%v", name, statErr, pathErr),
				file.Close(),
			)
		}
		m.files[name] = &heldModelFile{file: file, info: info}
	}
	manifestBody, err := readHeldFile(ctx, m.files[modelFilesManifestName].file)
	if err != nil {
		return nil, fmt.Errorf("read model file manifest: %w", err)
	}
	manifestDigest := sha256.Sum256(manifestBody)
	m.files[modelFilesManifestName].hash = hex.EncodeToString(manifestDigest[:])
	m.files[modelFilesManifestName].size = int64(len(manifestBody))
	var manifest modelFilesManifest
	if err := decodeStrictJSON(manifestBody, &manifest); err != nil {
		return nil, fmt.Errorf("parse model file manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Files) != 5 {
		return nil, fmt.Errorf("model file manifest schema/files are invalid")
	}
	wantOrder := committedModelNames[2:]
	for i, identity := range manifest.Files {
		if identity.Name != wantOrder[i] {
			return nil, fmt.Errorf("model file manifest file %d is %q, want %q", i, identity.Name, wantOrder[i])
		}
		if identity.Rows < 0 || identity.Size < 0 || !canonicalSHA256(identity.SHA256) {
			return nil, fmt.Errorf("model file manifest identity for %s is invalid", identity.Name)
		}
		if err := bindHeldIdentity(ctx, m.files[identity.Name], identity.fileContentIdentity); err != nil {
			return nil, err
		}
	}
	if manifest.Import.Name != importReportFileName {
		return nil, fmt.Errorf("model file manifest import is %q", manifest.Import.Name)
	}
	if manifest.Import.Size < 0 || !canonicalSHA256(manifest.Import.SHA256) {
		return nil, fmt.Errorf("model file manifest import identity is invalid")
	}
	if err := bindHeldIdentity(ctx, m.files[importReportFileName], manifest.Import); err != nil {
		return nil, err
	}
	reportBody, err := readHeldFile(ctx, m.files[importReportFileName].file)
	if err != nil {
		return nil, err
	}
	var report ingest.WikidataDumpImportReport
	if err := decodeStrictJSON(reportBody, &report); err != nil {
		return nil, fmt.Errorf("parse model import report: %w", err)
	}
	if err := ingest.ValidateWikidataDumpImportReport(report); err != nil {
		return nil, fmt.Errorf("validate model import report: %w", err)
	}
	paths := make(map[string]string, len(wantOrder))
	for _, name := range wantOrder {
		paths[name] = descriptorPath(m.files[name].file)
	}
	rows, err := validateModelPaths(ctx, paths, &report)
	if err != nil {
		return nil, fmt.Errorf("validate committed model: %w", err)
	}
	for _, identity := range manifest.Files {
		if rows[identity.Name] != identity.Rows {
			return nil, fmt.Errorf("model file %s rows %d, want %d", identity.Name, rows[identity.Name], identity.Rows)
		}
	}
	if rows["entity.parquet"] != int64(report.Accepted) || rows["reject.parquet"] != int64(report.Rejected) {
		return nil, fmt.Errorf("model/report row counts do not match accepted/rejected")
	}
	digest := sha256.Sum256(reportBody)
	m.version = "wikidata-" + hex.EncodeToString(digest[:6])
	if beforeFinalVerify != nil {
		beforeFinalVerify()
	}
	if err := m.Verify(ctx); err != nil {
		return nil, fmt.Errorf("final committed model verification: %w", err)
	}
	return m, nil
}

// MaterializeEntities reads the held model descriptors for HOT-tier baking.
func (m *CommittedModel) MaterializeEntities(ctx context.Context) ([]*model.Entity, error) {
	if err := m.Verify(ctx); err != nil {
		return nil, err
	}
	paths := make(map[string]string, 4)
	for _, name := range committedModelNames[2:6] {
		paths[name] = descriptorPath(m.files[name].file)
	}
	entities, err := materializeModelPaths(ctx, paths)
	if err != nil {
		return nil, err
	}
	if err := m.Verify(ctx); err != nil {
		return nil, fmt.Errorf("verify committed model after materialization: %w", err)
	}
	return entities, nil
}

func (m *CommittedModel) ModelVersion() string { return m.version }

func (m *CommittedModel) Verify(ctx context.Context) error {
	return m.verify(ctx, nil)
}

func (m *CommittedModel) verify(ctx context.Context, beforeFinalIdentity func()) error {
	if m == nil || m.closed {
		return fmt.Errorf("committed model is closed")
	}
	if err := m.verifyDirectory(ctx); err != nil {
		return err
	}
	if err := m.verifyFileIdentities(); err != nil {
		return err
	}
	for _, name := range committedModelNames {
		held := m.files[name]
		hash, size, err := hashHeldFile(ctx, held.file)
		if err != nil {
			return err
		}
		if hash != held.hash || size != held.size {
			return fmt.Errorf("committed model file %s content changed", name)
		}
	}
	if beforeFinalIdentity != nil {
		beforeFinalIdentity()
	}
	if err := m.verifyDirectory(ctx); err != nil {
		return err
	}
	return m.verifyFileIdentities()
}

func (m *CommittedModel) verifyDirectory(ctx context.Context) error {
	pathInfo, err := os.Lstat(m.path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, m.info) {
		return fmt.Errorf("committed model directory path changed")
	}
	if err := verifyCommittedEntries(ctx, m.root); err != nil {
		return err
	}
	return nil
}

func (m *CommittedModel) verifyFileIdentities() error {
	for _, name := range committedModelNames {
		held := m.files[name]
		info, err := held.file.Stat()
		if err != nil || !os.SameFile(info, held.info) || info.Size() != held.size || info.Mode() != held.info.Mode() || !info.ModTime().Equal(held.info.ModTime()) {
			return fmt.Errorf("committed model file %s changed", name)
		}
		pathInfo, err := m.root.Lstat(name)
		if err != nil || !os.SameFile(pathInfo, held.info) {
			return fmt.Errorf("committed model path %s changed", name)
		}
	}
	return nil
}

func verifyCommittedEntries(ctx context.Context, root *os.Root) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open committed model directory: %w", err)
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	entries, readErr := directory.ReadDir(len(committedModelNames) + 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("list committed model: %w", readErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(entries) > len(committedModelNames) {
		return fmt.Errorf("committed model has more than %d entries", len(committedModelNames))
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	slices.Sort(names)
	wantNames := slices.Clone(committedModelNames)
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		return fmt.Errorf("committed model entries %v, want %v", names, wantNames)
	}
	return nil
}

func (m *CommittedModel) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	var errs []error
	for _, held := range m.files {
		errs = append(errs, held.file.Close())
	}
	errs = append(errs, m.root.Close(), m.parent.Close())
	return errors.Join(errs...)
}

func bindHeldIdentity(ctx context.Context, held *heldModelFile, identity fileContentIdentity) error {
	hash, size, err := hashHeldFile(ctx, held.file)
	if err != nil {
		return err
	}
	if size != identity.Size || hash != identity.SHA256 {
		return fmt.Errorf("model file %s content identity mismatch", identity.Name)
	}
	held.hash, held.size = identity.SHA256, identity.Size
	return nil
}

func hashHeldFile(ctx context.Context, file *os.File) (string, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			written, writeErr := hash.Write(buffer[:n])
			size += int64(written)
			if writeErr != nil {
				return "", size, writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			if err := ctx.Err(); err != nil {
				return "", size, err
			}
			return hex.EncodeToString(hash.Sum(nil)), size, nil
		}
		if err != nil {
			return "", size, err
		}
	}
}

func readHeldFile(ctx context.Context, file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var body []byte
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			if len(body)+n > maxCommittedMetadataBytes {
				return nil, fmt.Errorf("committed model metadata exceeds %d bytes", maxCommittedMetadataBytes)
			}
			body = append(body, buffer[:n]...)
		}
		if errors.Is(err, io.EOF) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return body, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func canonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeStrictJSON(body []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(json.NewDecoder(bytes.NewReader(body)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	canonicalBody, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	var inputValue, canonicalValue any
	if err := json.Unmarshal(body, &inputValue); err != nil {
		return err
	}
	if err := json.Unmarshal(canonicalBody, &canonicalValue); err != nil {
		return err
	}
	return validateExactJSONNames(inputValue, canonicalValue)
}

func validateExactJSONNames(input, canonical any) error {
	switch value := input.(type) {
	case map[string]any:
		want, ok := canonical.(map[string]any)
		if !ok {
			return fmt.Errorf("JSON object does not match destination")
		}
		if len(value) != len(want) {
			return fmt.Errorf("JSON object has %d fields, want %d", len(value), len(want))
		}
		for name, child := range value {
			canonicalChild, ok := want[name]
			if !ok {
				return fmt.Errorf("non-canonical JSON field %q", name)
			}
			if err := validateExactJSONNames(child, canonicalChild); err != nil {
				return err
			}
		}
	case []any:
		want, ok := canonical.([]any)
		if !ok || len(value) != len(want) {
			return fmt.Errorf("JSON array does not match destination")
		}
		for i := range value {
			if err := validateExactJSONNames(value[i], want[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > maxCommittedJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxCommittedJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := rejectDuplicateJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := rejectDuplicateJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
