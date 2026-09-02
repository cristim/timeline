package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"wk/internal/blob"
	"wk/internal/duck"
	"wk/internal/ingest"
)

const (
	maxDumpImportManifestBytes  = 1 << 20
	maxCommittedDumpReportBytes = 16 << 20
)

var committedDumpArtifactNames = []string{
	"model-files.json", dumpImportReportName, "entity.parquet", "entity_category.parquet",
	"relationship.parquet", "claim.parquet", "reject.parquet",
}

type committedDumpBlob interface {
	PutReaderIfChanged(context.Context, string, string, io.ReaderAt, int64, string) (bool, error)
	PutIfAbsent(context.Context, string, string, []byte, string) (bool, error)
	GetBounded(context.Context, string, string, int64) ([]byte, error)
}

func publishOpenedCommittedWikidataDump(
	ctx context.Context,
	committed *duck.CommittedModel,
	client committedDumpBlob,
	bucket string,
	generatedAt time.Time,
) (manifest importManifest, err error) {
	if committed == nil {
		return importManifest{}, errors.New("publish committed Wikidata dump: nil model")
	}
	err = committed.VisitArtifacts(ctx, func(artifacts []duck.CommittedArtifact) error {
		if err := validateCommittedDumpArtifacts(artifacts); err != nil {
			return err
		}
		reportArtifact := artifacts[1]
		if reportArtifact.Size > maxCommittedDumpReportBytes {
			return fmt.Errorf("committed import report size %d exceeds %d", reportArtifact.Size, maxCommittedDumpReportBytes)
		}
		body, err := io.ReadAll(io.NewSectionReader(reportArtifact.Reader, 0, reportArtifact.Size))
		if err != nil {
			return fmt.Errorf("read committed import report: %w", err)
		}
		if int64(len(body)) != reportArtifact.Size {
			return fmt.Errorf("read committed import report: got %d bytes, want %d", len(body), reportArtifact.Size)
		}
		var report ingest.WikidataDumpImportReport
		if err := json.Unmarshal(body, &report); err != nil {
			return fmt.Errorf("parse committed import report: %w", err)
		}
		if err := ingest.ValidateWikidataDumpImportReport(report); err != nil {
			return fmt.Errorf("validate committed import report: %w", err)
		}
		manifest, err = publishCommittedWikidataDump(ctx, client, bucket, committed.ModelVersion(), report, artifacts, generatedAt, func() error {
			return committed.Verify(ctx)
		})
		return err
	})
	return manifest, err
}

func publishCommittedWikidataDump(
	ctx context.Context,
	client committedDumpBlob,
	bucket, dataset string,
	report ingest.WikidataDumpImportReport,
	artifacts []duck.CommittedArtifact,
	generatedAt time.Time,
	verify func() error,
) (importManifest, error) {
	if client == nil || verify == nil {
		return importManifest{}, fmt.Errorf("publish committed Wikidata dump: nil client or verifier")
	}
	if err := validateCommittedDumpArtifacts(artifacts); err != nil {
		return importManifest{}, err
	}
	reportManifest, err := publishCommittedDumpReport(ctx, client, bucket, dataset, report, artifacts[1], artifacts[6], generatedAt, verify)
	if err != nil {
		return importManifest{}, err
	}
	if err := publishCommittedWarmModel(ctx, client, bucket, dataset, artifacts[2:6], verify); err != nil {
		return importManifest{}, err
	}
	return reportManifest, nil
}

func validateCommittedDumpArtifacts(artifacts []duck.CommittedArtifact) error {
	if len(artifacts) != len(committedDumpArtifactNames) {
		return fmt.Errorf("publish committed Wikidata dump: got %d artifacts, want %d", len(artifacts), len(committedDumpArtifactNames))
	}
	for i, artifact := range artifacts {
		if artifact.Name != committedDumpArtifactNames[i] {
			return fmt.Errorf("publish committed Wikidata dump: artifact %d is %q, want %q", i, artifact.Name, committedDumpArtifactNames[i])
		}
		if artifact.Size < 0 || artifact.Rows < 0 || artifact.Reader == nil {
			return fmt.Errorf("publish committed Wikidata dump: artifact %s metadata is invalid", artifact.Name)
		}
	}
	return nil
}

func publishCommittedDumpReport(
	ctx context.Context,
	client committedDumpBlob,
	bucket, dataset string,
	report ingest.WikidataDumpImportReport,
	reportArtifact, rejectArtifact duck.CommittedArtifact,
	generatedAt time.Time,
	verify func() error,
) (importManifest, error) {
	rejectRows, err := artifactRowsAsInt(rejectArtifact)
	if err != nil {
		return importManifest{}, err
	}
	prefix := reportPrefix("wikidata-dump", dataset)
	manifest, body, err := buildImportManifest(prefix, generatedAt, report.SchemaVersion,
		publicationObject{Size: reportArtifact.Size, SHA256: reportArtifact.SHA256},
		importRejectObject{Size: rejectArtifact.Size, SHA256: rejectArtifact.SHA256, Rows: rejectRows})
	if err != nil {
		return importManifest{}, err
	}
	if _, err := client.PutReaderIfChanged(ctx, bucket, manifest.Rejects.Key, rejectArtifact.Reader, rejectArtifact.Size, parquetContentType); err != nil {
		return importManifest{}, fmt.Errorf("publish reject parquet: %w", err)
	}
	if _, err := client.PutReaderIfChanged(ctx, bucket, manifest.Report.Key, reportArtifact.Reader, reportArtifact.Size, "application/json"); err != nil {
		return importManifest{}, fmt.Errorf("publish import report: %w", err)
	}
	if err := verify(); err != nil {
		return importManifest{}, fmt.Errorf("verify committed model before import manifest: %w", err)
	}
	created, err := client.PutIfAbsent(ctx, bucket, prefix+"/manifest.json", body, "application/json")
	if err == nil && created {
		return manifest, nil
	}
	if err == nil {
		return importManifest{}, fmt.Errorf("create import manifest returned neither creation nor error")
	}
	if !errors.Is(err, blob.ErrPreconditionFailed) {
		return importManifest{}, fmt.Errorf("publish import manifest: %w", err)
	}
	winnerBody, err := client.GetBounded(ctx, bucket, prefix+"/manifest.json", maxDumpImportManifestBytes)
	if err != nil {
		return importManifest{}, fmt.Errorf("read winning import manifest: %w", err)
	}
	winner, err := validateWinningImportManifest(winnerBody, manifest)
	if err != nil {
		return importManifest{}, err
	}
	return winner, nil
}

func publishCommittedWarmModel(
	ctx context.Context,
	client committedDumpBlob,
	bucket, dataset string,
	artifacts []duck.CommittedArtifact,
	verify func() error,
) error {
	metadata := make([]warmModelFile, len(artifacts))
	for i, artifact := range artifacts {
		rows, err := artifactRowsAsInt(artifact)
		if err != nil {
			return err
		}
		metadata[i] = warmModelFile{Name: artifact.Name, Size: artifact.Size, SHA256: artifact.SHA256, Rows: rows}
	}
	manifest, body, err := buildWarmModelManifest(dataset, metadata)
	if err != nil {
		return err
	}
	for i, artifact := range artifacts {
		if _, err := client.PutReaderIfChanged(ctx, bucket, manifest.Files[i].Key, artifact.Reader, artifact.Size, parquetContentType); err != nil {
			return fmt.Errorf("publish model file %s: %w", artifact.Name, err)
		}
	}
	if err := verify(); err != nil {
		return fmt.Errorf("verify committed model before model manifest: %w", err)
	}
	manifestKey := fmt.Sprintf("model/%s/manifest.json", dataset)
	if _, err := client.PutReaderIfChanged(ctx, bucket, manifestKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return fmt.Errorf("publish model manifest: %w", err)
	}
	return nil
}

func artifactRowsAsInt(artifact duck.CommittedArtifact) (int, error) {
	rows := int(artifact.Rows)
	if int64(rows) != artifact.Rows {
		return 0, fmt.Errorf("artifact %s row count %d exceeds platform int", artifact.Name, artifact.Rows)
	}
	return rows, nil
}

func validateWinningImportManifest(body []byte, want importManifest) (importManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var winner importManifest
	if err := decoder.Decode(&winner); err != nil {
		return importManifest{}, fmt.Errorf("parse winning import manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return importManifest{}, fmt.Errorf("parse winning import manifest: %w", err)
	}
	canonical, err := json.Marshal(winner)
	if err != nil || !bytes.Equal(canonical, body) {
		return importManifest{}, fmt.Errorf("winning import manifest is not canonical JSON")
	}
	generatedAt, err := time.Parse(time.RFC3339, winner.GeneratedAt)
	if err != nil || generatedAt.UTC().Format(time.RFC3339) != winner.GeneratedAt {
		return importManifest{}, fmt.Errorf("winning import manifest generated_at is invalid")
	}
	if winner.SchemaVersion != want.SchemaVersion || winner.ImportID != want.ImportID ||
		winner.Report != want.Report || winner.Rejects != want.Rejects {
		return importManifest{}, fmt.Errorf("winning import manifest does not match committed report and rejects")
	}
	return winner, nil
}
