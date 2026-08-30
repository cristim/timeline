package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wk/internal/bake"
	"wk/internal/duck"
	"wk/internal/ingest"
)

type publicationObject struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type importRejectObject struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

type importManifest struct {
	SchemaVersion int                `json:"schema_version"`
	ImportID      string             `json:"import_id"`
	GeneratedAt   string             `json:"generated_at"`
	Report        publicationObject  `json:"report"`
	Rejects       importRejectObject `json:"rejects"`
}

func materializeImportArtifacts(ctx context.Context, dir string, result *ingest.Result, warmSource ingest.WarmSource, warmSHA256 string, ohm *ingest.OHMImportSummary) (ingest.ImportReport, duck.ModelFile, error) {
	report, err := ingest.BuildImportReport(result, warmSource, warmSHA256, ohm)
	if err != nil {
		return ingest.ImportReport{}, duck.ModelFile{}, fmt.Errorf("build import report: %w", err)
	}
	rejectFile, err := duck.WriteRejects(ctx, dir, rejectRows(result.Rejects))
	if err != nil {
		return ingest.ImportReport{}, duck.ModelFile{}, fmt.Errorf("write reject parquet: %w", err)
	}
	if rejectFile.Name != "reject.parquet" {
		return ingest.ImportReport{}, duck.ModelFile{}, fmt.Errorf("write reject parquet: unexpected file %q", rejectFile.Name)
	}
	if rejectFile.Path != filepath.Join(dir, rejectFile.Name) {
		return ingest.ImportReport{}, duck.ModelFile{}, fmt.Errorf("write reject parquet: unexpected path %q", rejectFile.Path)
	}
	if rejectFile.Rows != report.Rejected.Total {
		return ingest.ImportReport{}, duck.ModelFile{}, fmt.Errorf("write reject parquet: reject row count %d, want %d", rejectFile.Rows, report.Rejected.Total)
	}
	return report, rejectFile, nil
}

// rejectRows converts the ingest reject list into the reject-Parquet shape.
// Every importer writes the same table (DEV-5: "rejects are data").
func rejectRows(rejects []ingest.Reject) []duck.RejectRow {
	rows := make([]duck.RejectRow, 0, len(rejects))
	for _, reject := range rejects {
		rows = append(rows, duck.RejectRow{
			Source: string(reject.Source),
			File:   reject.File,
			Line:   reject.Line,
			Reason: reject.Reason,
		})
	}
	return rows
}

func publishImportArtifacts(ctx context.Context, sink bake.Sink, dataset string, generatedAt time.Time, report ingest.ImportReport, rejectFile duck.ModelFile) error {
	reportBody, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode import report: %w", err)
	}
	rejectBody, err := os.ReadFile(rejectFile.Path)
	if err != nil {
		return fmt.Errorf("read reject parquet: %w", err)
	}
	_, err = publishReportWithRejects(ctx, sink, reportPrefix("imports", dataset), generatedAt,
		report.SchemaVersion, reportBody, rejectBody, rejectFile.Rows)
	return err
}

// reportPrefix keeps every per-run report under wk-warm/reports/ (DEV-5).
func reportPrefix(kind, dataset string) string {
	return fmt.Sprintf("reports/%s/%s", kind, dataset)
}

// publishReportWithRejects writes the immutable, content-addressed report and
// reject table first, then repoints the manifest, so a reader never sees a
// pointer to an object that is not there yet (ARCH-2).
func publishReportWithRejects(
	ctx context.Context,
	sink bake.Sink,
	prefix string,
	generatedAt time.Time,
	schemaVersion int,
	reportBody, rejectBody []byte,
	rejectRowCount int,
) (importManifest, error) {
	reportDigest := sha256.Sum256(reportBody)
	rejectDigest := sha256.Sum256(rejectBody)
	identityBody, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		ReportSHA256  string `json:"report_sha256"`
		RejectSHA256  string `json:"reject_sha256"`
	}{
		SchemaVersion: schemaVersion,
		ReportSHA256:  fmt.Sprintf("%x", reportDigest),
		RejectSHA256:  fmt.Sprintf("%x", rejectDigest),
	})
	if err != nil {
		return importManifest{}, fmt.Errorf("encode import identity: %w", err)
	}
	importDigest := sha256.Sum256(identityBody)
	importID := fmt.Sprintf("%x", importDigest)

	manifest := importManifest{
		SchemaVersion: schemaVersion,
		ImportID:      importID,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		Report: publicationObject{
			Key:    fmt.Sprintf("%s/%s/report.json", prefix, importID),
			Size:   int64(len(reportBody)),
			SHA256: fmt.Sprintf("%x", reportDigest),
		},
		Rejects: importRejectObject{
			Key:    fmt.Sprintf("%s/%s/reject.parquet", prefix, importID),
			Size:   int64(len(rejectBody)),
			SHA256: fmt.Sprintf("%x", rejectDigest),
			Rows:   rejectRowCount,
		},
	}

	if _, err := sink.Put(ctx, manifest.Rejects.Key, rejectBody, parquetContentType); err != nil {
		return importManifest{}, fmt.Errorf("publish reject parquet: %w", err)
	}
	if _, err := sink.Put(ctx, manifest.Report.Key, reportBody, "application/json"); err != nil {
		return importManifest{}, fmt.Errorf("publish import report: %w", err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return importManifest{}, fmt.Errorf("encode import manifest: %w", err)
	}
	if _, err := sink.Put(ctx, prefix+"/manifest.json", manifestBody, "application/json"); err != nil {
		return importManifest{}, fmt.Errorf("publish import manifest: %w", err)
	}
	return manifest, nil
}
