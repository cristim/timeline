package duck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wk/internal/ingest"
)

func committedModelFixture(t *testing.T) (string, ingest.WikidataDumpImportReport) {
	t.Helper()
	body, err := os.ReadFile("../ingest/testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "model")
	sink, err := NewWikidataDumpStagingSink(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ingest.ImportWikidataDumpToSink(bytes.NewReader(body), ingest.WikidataDumpImportOptions{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	reportBody, _ := json.Marshal(report)
	if err := sink.Finalize(context.Background(), reportBody); err != nil {
		t.Fatal(err)
	}
	return out, report
}

func TestOpenCommittedModelValidatesAndRetainsDescriptors(t *testing.T) {
	dir, report := committedModelFixture(t)
	opened, err := OpenCommittedModel(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenCommittedModel: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	entities, err := opened.MaterializeEntities(context.Background())
	if err != nil {
		t.Fatalf("MaterializeEntities: %v", err)
	}
	if len(entities) != report.Accepted || !strings.HasPrefix(opened.ModelVersion(), "wikidata-") {
		t.Fatalf("opened model entities/version = %d/%q", len(entities), opened.ModelVersion())
	}
	if err := opened.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestOpenCommittedModelRejectsExtraAndHashValidReportCountMismatch(t *testing.T) {
	t.Run("extra", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil {
			t.Fatal("accepted extra file")
		}
	})
	t.Run("report count", func(t *testing.T) {
		dir, report := committedModelFixture(t)
		report.Accepted--
		report.Filtered++
		report.FilterReasons[0].Count++
		report.FilterReasons[0].Rate = math.Round(float64(report.FilterReasons[0].Count)/float64(report.Items)*1e6) / 1e6
		report.AcceptedByType[0].Count--
		body, _ := json.Marshal(report)
		if err := os.WriteFile(filepath.Join(dir, importReportFileName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		updateImportIdentity(t, dir)
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "does not match import report") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing report field", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, importReportFileName)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report map[string]any
		if err := json.Unmarshal(body, &report); err != nil {
			t.Fatal(err)
		}
		delete(report, "properties")
		body, _ = json.Marshal(report)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		updateImportIdentity(t, dir)
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "fields, want") {
			t.Fatalf("error = %v, want missing-field failure", err)
		}
	})
	t.Run("missing manifest field", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, modelFilesManifestName)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		delete(manifest, "schema_version")
		body, _ = json.Marshal(manifest)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "fields, want") {
			t.Fatalf("error = %v, want missing-field failure", err)
		}
	})
}

func TestVerifyCommittedEntriesHonorsCancellation(t *testing.T) {
	dir, _ := committedModelFixture(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyCommittedEntries(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyCommittedEntries error = %v, want context cancellation", err)
	}
}

func TestOpenCommittedModelRejectsParquetIdentityAndLogicalCorruption(t *testing.T) {
	t.Run("manifest hash mismatch", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		rewriteParquet(t, filepath.Join(dir, "entity.parquet"), `SELECT * FROM read_parquet(?)`)
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "content identity mismatch") {
			t.Fatalf("error = %v, want content identity mismatch", err)
		}
	})
	t.Run("rehashed empty slug", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, "entity.parquet")
		rewriteParquet(t, path, `SELECT * REPLACE ('' AS slug) FROM read_parquet(?)`)
		updateManifestIdentity(t, dir, "entity.parquet")
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "empty slug") {
			t.Fatalf("error = %v, want empty slug", err)
		}
	})
	for _, wikidata := range []string{"", "Q999999999"} {
		t.Run("rehashed Wikidata identity "+wikidata, func(t *testing.T) {
			dir, _ := committedModelFixture(t)
			path := filepath.Join(dir, "entity.parquet")
			rewriteParquet(t, path, fmt.Sprintf(`SELECT * REPLACE ('%s' AS wikidata) FROM read_parquet(?)`, wikidata))
			updateManifestIdentity(t, dir, "entity.parquet")
			if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "Wikidata identity") {
				t.Fatalf("error = %v, want Wikidata identity failure", err)
			}
		})
	}
	t.Run("rehashed reject schema", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, "reject.parquet")
		rewriteParquet(t, path, `SELECT source AS wrong_source, file, line, reason FROM read_parquet(?)`)
		updateManifestIdentity(t, dir, "reject.parquet")
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "query rejects") {
			t.Fatalf("error = %v, want reject schema failure", err)
		}
	})
	t.Run("rehashed orphan category", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, "entity_category.parquet")
		rewriteParquet(t, path, `SELECT * FROM read_parquet(?) UNION ALL SELECT 'missing', 0, 'war'`)
		updateManifestIdentity(t, dir, "entity_category.parquet")
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "category owner") {
			t.Fatalf("error = %v, want category owner failure", err)
		}
	})
	t.Run("rehashed entity type distribution", func(t *testing.T) {
		dir, _ := committedModelFixture(t)
		path := filepath.Join(dir, "entity.parquet")
		rewriteParquet(t, path, `SELECT * REPLACE (CASE WHEN input_order = 0 THEN CASE WHEN type = 'event' THEN 'place' ELSE 'event' END ELSE type END AS type) FROM read_parquet(?)`)
		updateManifestIdentity(t, dir, "entity.parquet")
		if _, err := OpenCommittedModel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "does not match import report") {
			t.Fatalf("error = %v, want report type-distribution failure", err)
		}
	})
}

func TestOpenCommittedModelRunsFinalVerification(t *testing.T) {
	for _, kind := range []string{"mutation", "extra entry"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := committedModelFixture(t)
			opened, err := openCommittedModel(context.Background(), dir, func() {
				if kind == "extra entry" {
					if writeErr := os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o600); writeErr != nil {
						t.Fatal(writeErr)
					}
					return
				}
				file, openErr := os.OpenFile(filepath.Join(dir, "entity.parquet"), os.O_WRONLY, 0)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if _, writeErr := file.WriteAt([]byte("changed"), 0); writeErr != nil {
					t.Fatal(writeErr)
				}
				if closeErr := file.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
			})
			if opened != nil || err == nil || !strings.Contains(err.Error(), "final committed model verification") {
				t.Fatalf("OpenCommittedModel = %v, %v; want final verification error", opened, err)
			}
		})
	}
}

func updateManifestIdentity(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	manifestPath := filepath.Join(dir, modelFilesManifestName)
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest modelFilesManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Files {
		if manifest.Files[i].Name == name {
			manifest.Files[i].SHA256 = hex.EncodeToString(digest[:])
			manifest.Files[i].Size = int64(len(body))
		}
	}
	manifestBody, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBody, 0o644); err != nil {
		t.Fatal(err)
	}
}

func updateImportIdentity(t *testing.T, dir string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, importReportFileName))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	manifestPath := filepath.Join(dir, modelFilesManifestName)
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest modelFilesManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Import.SHA256, manifest.Import.Size = hex.EncodeToString(digest[:]), int64(len(body))
	manifestBody, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBody, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCommittedModelRejectsMissingSymlinkAndNonregularArtifacts(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := committedModelFixture(t)
			path := filepath.Join(dir, "entity.parquet")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "entity.parquet")
				if err := os.WriteFile(target, []byte("parquet"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := OpenCommittedModel(context.Background(), dir); err == nil {
				t.Fatalf("accepted %s artifact", kind)
			}
		})
	}
}

func TestOpenCommittedModelRejectsSymlinkedDirectory(t *testing.T) {
	dir, _ := committedModelFixture(t)
	link := filepath.Join(t.TempDir(), "model")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCommittedModel(context.Background(), link); err == nil {
		t.Fatal("accepted symlinked model directory")
	}
}

func TestCommittedModelVerifyRejectsPathReplacementAndMutation(t *testing.T) {
	for _, kind := range []string{"symlink", "mutation"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := committedModelFixture(t)
			opened, err := OpenCommittedModel(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })
			path := filepath.Join(dir, "entity.parquet")
			if kind == "symlink" {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("entity.parquet.old", path); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteAt([]byte("changed"), 0); err != nil {
					t.Fatal(err)
				}
				_ = file.Close()
			}
			if err := opened.Verify(context.Background()); err == nil {
				t.Fatal("Verify accepted changed model")
			}
		})
	}
}

func TestCommittedModelVerifyRepeatsFinalPathIdentityChecks(t *testing.T) {
	for _, kind := range []string{"extra entry", "directory replacement", "file replacement", "in-place mutation"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := committedModelFixture(t)
			opened, err := OpenCommittedModel(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })
			err = opened.verify(context.Background(), func() {
				switch kind {
				case "extra entry":
					if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "directory replacement":
					if err := os.Rename(dir, dir+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				case "file replacement":
					path := filepath.Join(dir, "entity.parquet")
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, body, 0o600); err != nil {
						t.Fatal(err)
					}
				case "in-place mutation":
					path := filepath.Join(dir, "entity.parquet")
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					file, err := os.OpenFile(path, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.WriteAt([]byte("changed"), 0); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					changedTime := info.ModTime().Add(time.Second)
					if err := os.Chtimes(path, changedTime, changedTime); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil {
				t.Fatalf("Verify accepted %s after descriptor hashes", kind)
			}
		})
	}
}

func TestDecodeStrictJSONRejectsUnknownDuplicateAndTrailingData(t *testing.T) {
	for _, body := range []string{`{}`, `{"name":"x","unknown":1}`, `{"name":"x","name":"y"}`, `{"Name":"x"}`, `{"Name":"x","name":"y"}`, `{"name":"x"} {}`} {
		var value struct {
			Name string `json:"name"`
		}
		if err := decodeStrictJSON([]byte(body), &value); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	deep := strings.Repeat("[", maxCommittedJSONDepth+1) + "0" + strings.Repeat("]", maxCommittedJSONDepth+1)
	var value any
	if err := decodeStrictJSON([]byte(deep), &value); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deep JSON error = %v", err)
	}
}

func TestValidateCommittedRejectRowsChecksProvenanceOrderAndInputPosition(t *testing.T) {
	for _, kind := range []string{"source", "order", "duplicate", "range"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			file, err := WriteRejects(context.Background(), dir, []RejectRow{
				{Source: string(ingest.RejectSourceWikidataDump), File: "wikidata", Line: 1, Reason: "a"},
				{Source: string(ingest.RejectSourceWikidataDump), File: "wikidata", Line: 2, Reason: "b"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "source" {
				rewriteParquet(t, file.Path, `SELECT * REPLACE ('other' AS source) FROM read_parquet(?)`)
			} else if kind == "order" {
				rewriteParquet(t, file.Path, `SELECT * FROM read_parquet(?) ORDER BY line DESC`)
			} else if kind == "duplicate" {
				rewriteParquet(t, file.Path, `SELECT * REPLACE (1 AS line) FROM read_parquet(?)`)
			} else {
				rewriteParquet(t, file.Path, `SELECT * REPLACE (3 AS line) FROM read_parquet(?)`)
			}
			db, conn, err := openConnection(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = conn.Close()
				_ = db.Close()
			})
			report := ingest.WikidataDumpImportReport{Source: "wikidata", Items: 3}
			if _, err := validateRejectRows(context.Background(), conn, file.Path, &report); err == nil {
				t.Fatalf("accepted invalid reject %s", kind)
			}
		})
	}
}
