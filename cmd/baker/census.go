package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/ingest"
)

const censusManifestKey = "reports/census/manifest.json"

type censusOptions struct {
	seedDir         string
	seedOnly        bool
	warmFile        string
	wikidataDump    string
	wikidataDumpSet bool
}

type censusRunner struct {
	stdout   io.Writer
	now      func() time.Time
	sink     bake.Sink
	loadWarm func(context.Context, censusOptions) ([]byte, ingest.WarmSource, error)
}

type censusManifest struct {
	SchemaVersion int               `json:"schema_version"`
	ReportID      string            `json:"report_id"`
	GeneratedAt   string            `json:"generated_at"`
	Report        publicationObject `json:"report"`
}

func runCensus(ctx context.Context, args []string) error {
	return runCensusWithIO(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}

func runCensusWithIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts, err := parseCensusArgs(args, stderr)
	if err != nil {
		return err
	}
	if opts.wikidataDumpSet {
		return runWikidataDumpCoverage(opts.wikidataDump, stdin, stdout)
	}

	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}
	warmBucket := envOr("BUCKET_WARM", "wk-warm")
	runner := censusRunner{
		stdout: stdout,
		now:    time.Now,
		sink:   blob.BucketSink{Client: cli, Bucket: warmBucket},
		loadWarm: func(ctx context.Context, opts censusOptions) ([]byte, ingest.WarmSource, error) {
			switch {
			case opts.seedOnly:
				return nil, ingest.WarmSourceNone, nil
			case opts.warmFile != "":
				body, err := os.ReadFile(opts.warmFile)
				if err != nil {
					return nil, "", fmt.Errorf("read --warm-file: %w", err)
				}
				return body, ingest.WarmSourceWarmFile, nil
			default:
				body, err := cli.Get(ctx, warmBucket, warmEventsKey)
				if err != nil {
					return nil, "", fmt.Errorf("load default warm input %s: %w", warmEventsKey, err)
				}
				return body, ingest.WarmSourceWikidataEvents, nil
			}
		},
	}
	return runCensusWithRunner(ctx, opts, runner)
}

func runWikidataDumpCoverage(path string, stdin io.Reader, stdout io.Writer) error {
	r := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open --wikidata-dump %q: %w", path, err)
		}
		defer file.Close()
		r = file
	}

	report, err := ingest.BuildWikidataDumpCoverageReport(r)
	if err != nil {
		return fmt.Errorf("scan --wikidata-dump %q: %w", path, err)
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return fmt.Errorf("encode --wikidata-dump report: %w", err)
	}
	return nil
}

func parseCensusArgs(args []string, output io.Writer) (censusOptions, error) {
	fs := flag.NewFlagSet("census", flag.ContinueOnError)
	fs.SetOutput(output)

	seedDir := fs.String("seed", "data/seed", "NDJSON seed directory")
	seedOnly := fs.Bool("seed-only", false, "publish a seed-only census; skip every warm input")
	warmFile := fs.String("warm-file", "", "read warm-events NDJSON from this local file instead of BUCKET_WARM/"+warmEventsKey)
	wikidataDump := fs.String("wikidata-dump", "", "stream a decoded Wikidata JSON dump from this path or -")
	if err := fs.Parse(args); err != nil {
		return censusOptions{}, err
	}
	seedSet := false
	wikidataDumpSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "seed":
			seedSet = true
		case "wikidata-dump":
			wikidataDumpSet = true
		}
	})
	if wikidataDumpSet && *wikidataDump == "" {
		return censusOptions{}, fmt.Errorf("--wikidata-dump requires a non-empty path or -")
	}
	if wikidataDumpSet && (seedSet || *seedOnly || *warmFile != "") {
		return censusOptions{}, fmt.Errorf("--wikidata-dump is mutually exclusive with --seed, --seed-only, and --warm-file")
	}
	if *seedOnly && *warmFile != "" {
		return censusOptions{}, fmt.Errorf("--seed-only and --warm-file are mutually exclusive")
	}
	return censusOptions{
		seedDir:         *seedDir,
		seedOnly:        *seedOnly,
		warmFile:        *warmFile,
		wikidataDump:    *wikidataDump,
		wikidataDumpSet: wikidataDumpSet,
	}, nil
}

func runCensusWithRunner(ctx context.Context, opts censusOptions, runner censusRunner) error {
	if runner.stdout == nil {
		runner.stdout = io.Discard
	}
	if runner.now == nil {
		runner.now = time.Now
	}
	if runner.sink == nil {
		return fmt.Errorf("run census: nil sink")
	}
	if runner.loadWarm == nil {
		return fmt.Errorf("run census: nil warm loader")
	}

	res, err := ingest.LoadSeed(opts.seedDir)
	if err != nil {
		return err
	}

	warm, warmSource, err := runner.loadWarm(ctx, opts)
	if err != nil {
		return err
	}

	warmSHA256 := ""
	if warmSource != ingest.WarmSourceNone {
		sum := sha256.Sum256(warm)
		warmSHA256 = fmt.Sprintf("%x", sum[:])
		added, skipped, err := ingest.MergeWarmEvents(res, warm)
		if err != nil {
			return err
		}
		fmt.Fprintf(runner.stdout, "warm events: %d merged, %d deduped against seed\n", added, skipped)
	}

	report, err := ingest.BuildCensusReport(res, warmSource, warmSHA256)
	if err != nil {
		return err
	}

	printCensusSummary(runner.stdout, report)
	manifest, err := publishCensusReport(ctx, runner.sink, runner.now().UTC(), report)
	if err != nil {
		return err
	}
	fmt.Fprintf(runner.stdout, "report -> s3://%s/%s\n", envOr("BUCKET_WARM", "wk-warm"), manifest.Report.Key)
	return nil
}

func printCensusSummary(out io.Writer, report ingest.CensusReport) {
	fmt.Fprintf(out, "\n%-14s %7s %7s %7s %7s  %s\n", "CENTURY", "COUNT", "DATE", "COORD", "WIKI", "TYPES")
	for _, row := range report.Centuries {
		fmt.Fprintf(
			out,
			"%-14s %7d %7d %7d %7d  %s\n",
			formatCentury(row.CenturyStartYear),
			row.Total.Count,
			row.Total.HasDate,
			row.Total.HasCoordinates,
			row.Total.HasEnglishWikipedia,
			summarizeCensusTypes(row.Types),
		)
	}
	fmt.Fprintf(out, "%-14s %7d %7d %7d %7d\n", "TOTAL", report.Total.Count, report.Total.HasDate, report.Total.HasCoordinates, report.Total.HasEnglishWikipedia)
}

func formatCentury(start float64) string {
	return fmt.Sprintf("%.0f", start)
}

func summarizeCensusTypes(rows []ingest.CensusTypeRow) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s:%d", row.Type, row.Stats.Count))
	}
	return strings.Join(parts, ", ")
}

func publishCensusReport(ctx context.Context, sink bake.Sink, generatedAt time.Time, report ingest.CensusReport) (censusManifest, error) {
	reportBody, err := json.Marshal(report)
	if err != nil {
		return censusManifest{}, fmt.Errorf("encode census report: %w", err)
	}
	reportDigest := sha256.Sum256(reportBody)
	reportID := fmt.Sprintf("%x", reportDigest)
	manifest := censusManifest{
		SchemaVersion: ingest.CensusReportSchemaVersion,
		ReportID:      reportID,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		Report: publicationObject{
			Key:    fmt.Sprintf("reports/census/%s/report.json", reportID),
			Size:   int64(len(reportBody)),
			SHA256: reportID,
		},
	}

	if _, err := sink.Put(ctx, manifest.Report.Key, reportBody, "application/json"); err != nil {
		return censusManifest{}, fmt.Errorf("publish census report: %w", err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return censusManifest{}, fmt.Errorf("encode census manifest: %w", err)
	}
	if _, err := sink.Put(ctx, censusManifestKey, manifestBody, "application/json"); err != nil {
		return censusManifest{}, fmt.Errorf("publish census manifest: %w", err)
	}
	return manifest, nil
}
