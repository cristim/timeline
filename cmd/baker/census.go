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

const (
	censusReportPrefix = "reports/census"
	censusManifestKey  = censusReportPrefix + "/manifest.json"
)

type censusOptions struct {
	seedDir         string
	seedOnly        bool
	warmFile        string
	wikidataDump    string
	wikidataDumpSet bool
	publish         bool
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
		return runWikidataDumpCoverage(ctx, opts, stdin, stdout, stderr)
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

func runWikidataDumpCoverage(ctx context.Context, opts censusOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	path := opts.wikidataDump
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
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode --wikidata-dump report: %w", err)
	}
	if _, err := stdout.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write --wikidata-dump report: %w", err)
	}
	if !opts.publish {
		return nil
	}

	// The ROAD-2 census belongs beside the artifacts it sizes: immutable,
	// content-addressed, in wk-warm/reports/ (DEV-5, SRC-5).
	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}
	warmBucket := envOr("BUCKET_WARM", "wk-warm")
	prefix := reportPrefix("wikidata-census", dumpContentVersion(body))
	manifest, err := publishContentAddressedReport(ctx,
		blob.BucketSink{Client: cli, Bucket: warmBucket}, prefix, prefix+"/manifest.json",
		time.Now().UTC(), report.SchemaVersion, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "report -> s3://%s/%s\n", warmBucket, manifest.Report.Key)
	return nil
}

func parseCensusArgs(args []string, output io.Writer) (censusOptions, error) {
	fs := flag.NewFlagSet("census", flag.ContinueOnError)
	fs.SetOutput(output)

	seedDir := fs.String("seed", "data/seed", "NDJSON seed directory")
	seedOnly := fs.Bool("seed-only", false, "publish a seed-only census; skip every warm input")
	warmFile := fs.String("warm-file", "", "read warm-events NDJSON from this local file instead of BUCKET_WARM/"+warmEventsKey)
	wikidataDump := fs.String("wikidata-dump", "", "stream a Wikidata JSON dump from this path or - (.json, .json.gz, .json.bz2)")
	publish := fs.Bool("publish", false, "with --wikidata-dump, also publish the census to BUCKET_WARM/reports/")
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
	if *publish && !wikidataDumpSet {
		// The seed census always publishes; the flag would be a no-op there.
		return censusOptions{}, fmt.Errorf("--publish only applies to --wikidata-dump")
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
		publish:         *publish,
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
	fmt.Fprintf(out, "\n%-16s %10s %7s %7s %7s %7s  %s\n", "FROM YEAR", "SPAN", "COUNT", "DATE", "COORD", "WIKI", "TYPES")
	for _, row := range report.Buckets {
		fmt.Fprintf(
			out,
			"%-16s %10s %7d %7d %7d %7d  %s\n",
			formatCensusYear(row.StartYear),
			formatCensusYear(row.SpanYears),
			row.Total.Count,
			row.Total.HasDate,
			row.Total.HasCoordinates,
			row.Total.HasEnglishWikipedia,
			summarizeCensusTypes(row.Types),
		)
	}
	fmt.Fprintf(out, "%-16s %10s %7d %7d %7d %7d\n", "TOTAL", "",
		report.Total.Count, report.Total.HasDate, report.Total.HasCoordinates, report.Total.HasEnglishWikipedia)
}

func formatCensusYear(year float64) string {
	return fmt.Sprintf("%.0f", year)
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
	return publishContentAddressedReport(ctx, sink, censusReportPrefix, censusManifestKey,
		generatedAt, ingest.CensusReportSchemaVersion, reportBody)
}

// publishContentAddressedReport writes the report under its own digest, then
// repoints the manifest. The immutable object always exists before anything
// names it (ARCH-2), and republishing the same report is a no-op on the
// immutable side however often it runs.
func publishContentAddressedReport(
	ctx context.Context,
	sink bake.Sink,
	prefix, manifestKey string,
	generatedAt time.Time,
	schemaVersion int,
	reportBody []byte,
) (censusManifest, error) {
	reportDigest := sha256.Sum256(reportBody)
	reportID := fmt.Sprintf("%x", reportDigest)
	manifest := censusManifest{
		SchemaVersion: schemaVersion,
		ReportID:      reportID,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		Report: publicationObject{
			Key:    fmt.Sprintf("%s/%s/report.json", prefix, reportID),
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
	if _, err := sink.Put(ctx, manifestKey, manifestBody, "application/json"); err != nil {
		return censusManifest{}, fmt.Errorf("publish census manifest: %w", err)
	}
	return manifest, nil
}
