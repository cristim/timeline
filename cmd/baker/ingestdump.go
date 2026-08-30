package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"wk/internal/blob"
	"wk/internal/duck"
	"wk/internal/ingest"
)

// The ROAD-3 step-2 stage: a Wikidata dump becomes the normalized Parquet model
// in wk-warm, with the per-run report and reject table DEV-5 asks for. The bake
// then reads that model instead of the seed (see bake --model).

// dumpImportReportName is the report written beside the model files. It is what
// makes a model directory self-describing: `bake --model` reads it for the
// dataset version, so a bake can always name the dump it came from.
const dumpImportReportName = "import.json"

type ingestDumpOptions struct {
	dump            string
	out             string
	importanceFloor float64
	maxRejectRate   float64
	publish         bool
}

func runIngestWikidataDump(ctx context.Context, args []string) error {
	return runIngestWikidataDumpWithIO(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}

func runIngestWikidataDumpWithIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts, err := parseIngestDumpArgs(args, stderr)
	if err != nil {
		return err
	}

	source := stdin
	if opts.dump != "-" {
		file, err := os.Open(opts.dump)
		if err != nil {
			return fmt.Errorf("open --dump %q: %w", opts.dump, err)
		}
		defer file.Close()
		source = file
	}

	imported, err := ingest.ImportWikidataDump(source, ingest.WikidataDumpImportOptions{
		MaxRejectRate:   opts.maxRejectRate,
		ImportanceFloor: opts.importanceFloor,
	})
	if err != nil {
		return err
	}

	modelFiles, err := duck.WriteModel(ctx, opts.out, imported.Entities)
	if err != nil {
		return fmt.Errorf("materialize model: %w", err)
	}
	rejectFile, err := duck.WriteRejects(ctx, opts.out, rejectRows(imported.Rejects))
	if err != nil {
		return fmt.Errorf("write reject parquet: %w", err)
	}
	reportBody, err := json.Marshal(imported.Report)
	if err != nil {
		return fmt.Errorf("encode import report: %w", err)
	}
	reportPath := filepath.Join(opts.out, dumpImportReportName)
	if err := os.WriteFile(reportPath, reportBody, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", reportPath, err)
	}

	printDumpImportSummary(stdout, imported.Report, modelFiles, rejectFile)
	fmt.Fprintf(stdout, "model %s -> %s\n", dumpModelVersion(reportBody), opts.out)

	if !opts.publish {
		return nil
	}
	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}
	warmBucket := envOr("BUCKET_WARM", "wk-warm")
	sink := blob.BucketSink{Client: cli, Bucket: warmBucket}
	dataset := datasetVersion()

	rejectBody, err := os.ReadFile(rejectFile.Path)
	if err != nil {
		return fmt.Errorf("read reject parquet: %w", err)
	}
	manifest, err := publishReportWithRejects(ctx, sink, reportPrefix("wikidata-dump", dataset),
		time.Now().UTC(), imported.Report.SchemaVersion, reportBody, rejectBody, rejectFile.Rows)
	if err != nil {
		return err
	}
	if err := publishWarmModel(ctx, sink, dataset, modelFiles); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "report -> s3://%s/%s\n", warmBucket, manifest.Report.Key)
	fmt.Fprintf(stdout, "model  -> s3://%s/model/%s/\n", warmBucket, dataset)
	return nil
}

func parseIngestDumpArgs(args []string, output io.Writer) (ingestDumpOptions, error) {
	fs := flag.NewFlagSet("ingest-wikidata-dump", flag.ContinueOnError)
	fs.SetOutput(output)

	dump := fs.String("dump", "", "Wikidata JSON dump to read: a path (.json, .json.gz, .json.bz2) or - for stdin")
	out := fs.String("out", "", "directory to write the normalized Parquet model, reject table and import report into")
	importanceFloor := fs.Float64("importance-floor", 0,
		"keep only entities at or above this importance; the rest stay WARM (SRC-5)")
	maxRejectRate := fs.Float64("max-reject-rate", 0,
		"fail if more than this fraction of normalizable items are rejected (0 uses the default gate)")
	publish := fs.Bool("publish", false, "publish the model and report to BUCKET_WARM")
	if err := fs.Parse(args); err != nil {
		return ingestDumpOptions{}, err
	}
	if *dump == "" {
		return ingestDumpOptions{}, fmt.Errorf("--dump <path|-> is required")
	}
	if *out == "" {
		return ingestDumpOptions{}, fmt.Errorf("--out <dir> is required")
	}
	return ingestDumpOptions{
		dump:            *dump,
		out:             *out,
		importanceFloor: *importanceFloor,
		maxRejectRate:   *maxRejectRate,
		publish:         *publish,
	}, nil
}

func printDumpImportSummary(out io.Writer, report ingest.WikidataDumpImportReport, modelFiles []duck.ModelFile, rejectFile duck.ModelFile) {
	fmt.Fprintf(out, "wikidata dump %s (%s): %d items, %d accepted, %d filtered, %d rejected (rate %.4f, gate %.4f)\n",
		report.InputSHA256[:12], report.Compression,
		report.Items, report.Accepted, report.Filtered, report.Rejected, report.RejectRate, report.MaxRejectRate)
	for _, row := range report.FilterReasons {
		fmt.Fprintf(out, "  filtered %7d (%.4f) %s\n", row.Count, row.Rate, row.Reason)
	}
	for _, row := range report.RejectReasons {
		fmt.Fprintf(out, "  rejected %7d (%.4f) %s\n", row.Count, row.Rate, row.Reason)
	}
	for _, row := range report.AcceptedByType {
		fmt.Fprintf(out, "  type     %7d %s\n", row.Count, row.Type)
	}
	for _, file := range modelFiles {
		fmt.Fprintf(out, "  parquet  %7d rows %s\n", file.Rows, file.Name)
	}
	fmt.Fprintf(out, "  parquet  %7d rows %s\n", rejectFile.Rows, rejectFile.Name)
}

// dumpModelVersion names the dataset a model directory produces. It is the
// content address of the import report, so the same dump and the same code
// always name the same version, and a different dump never reuses one.
func dumpModelVersion(reportBody []byte) string {
	digest := sha256.Sum256(reportBody)
	return fmt.Sprintf("wikidata-%x", digest[:6])
}

// loadDumpModelVersion reads the report a model directory was written with.
func loadDumpModelVersion(dir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, dumpImportReportName))
	if err != nil {
		return "", fmt.Errorf("read model import report: %w", err)
	}
	var report ingest.WikidataDumpImportReport
	if err := json.Unmarshal(body, &report); err != nil {
		return "", fmt.Errorf("parse model import report: %w", err)
	}
	if report.SchemaVersion != ingest.WikidataDumpImportReportSchemaVersion {
		return "", fmt.Errorf("model import report schema %d, want %d",
			report.SchemaVersion, ingest.WikidataDumpImportReportSchemaVersion)
	}
	return dumpModelVersion(body), nil
}
