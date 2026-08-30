package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wk/internal/bake"
	"wk/internal/ingest"
)

const expectedWikidataDumpCoverageJSON = `{"schema_version":1,"coverage_basis":"wikidata-item-facts-after-statement-validation-before-type-classification","input_sha256":"3d59b3bde012de266b41ffadc982e24eb820f8d3297d63130ae61851e49af6d4",` +
	`"items":{"count":2,"has_english_label":1,"has_date":1,"has_coordinates":1,"has_english_wikipedia":1,"has_any_sitelink":2,"has_all":1,"total_sitelinks":4},` +
	`"time_claims":[{"property":"P569","precision":11,"count":1},{"property":"P570","precision":9,"count":1},{"property":"P577","precision":10,"count":1},{"property":"P580","precision":11,"count":1},{"property":"P585","precision":7,"count":1}]}` + "\n"

type censusErrorWriter struct {
	err error
}

func (w censusErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPublishCensusReportUsesContentAddressedKeysAndWritesManifestLast(t *testing.T) {
	t.Parallel()

	report := testCensusReport(t)
	first := new(recordingSink)
	firstTime := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	firstManifest, err := publishCensusReport(context.Background(), first, firstTime, report)
	if err != nil {
		t.Fatalf("publishCensusReport: %v", err)
	}
	second := new(recordingSink)
	secondTime := firstTime.Add(5 * time.Minute)
	secondManifest, err := publishCensusReport(context.Background(), second, secondTime, report)
	if err != nil {
		t.Fatalf("second publishCensusReport: %v", err)
	}

	if len(first.puts) != 2 {
		t.Fatalf("Put calls = %d, want 2", len(first.puts))
	}
	if first.puts[0].contentType != "application/json" || first.puts[1].contentType != "application/json" {
		t.Fatalf("content types = %#v", first.puts)
	}
	if first.puts[1].key != censusManifestKey {
		t.Fatalf("last key = %q, want %q", first.puts[1].key, censusManifestKey)
	}

	decodedFirst := decodeCensusManifest(t, first.puts[1].body)
	if decodedFirst != firstManifest {
		t.Fatalf("decoded manifest = %#v, want %#v", decodedFirst, firstManifest)
	}
	if firstManifest.SchemaVersion != ingest.CensusReportSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", firstManifest.SchemaVersion, ingest.CensusReportSchemaVersion)
	}
	if firstManifest.GeneratedAt != firstTime.Format(time.RFC3339) {
		t.Fatalf("GeneratedAt = %q, want %q", firstManifest.GeneratedAt, firstTime.Format(time.RFC3339))
	}
	if firstManifest.Report.Key != "reports/census/"+firstManifest.ReportID+"/report.json" {
		t.Fatalf("report key = %q", firstManifest.Report.Key)
	}
	if firstManifest.Report.Size != int64(len(first.puts[0].body)) {
		t.Fatalf("report size = %d, want %d", firstManifest.Report.Size, len(first.puts[0].body))
	}
	if firstManifest.Report.SHA256 != fmt.Sprintf("%x", sha256Bytes(first.puts[0].body)) {
		t.Fatalf("report sha256 = %q", firstManifest.Report.SHA256)
	}
	if firstManifest.ReportID != firstManifest.Report.SHA256 {
		t.Fatalf("report id = %q, want %q", firstManifest.ReportID, firstManifest.Report.SHA256)
	}
	if first.puts[0].key != firstManifest.Report.Key {
		t.Fatalf("immutable key = %q, want %q", first.puts[0].key, firstManifest.Report.Key)
	}

	decodedSecond := decodeCensusManifest(t, second.puts[1].body)
	if firstManifest.ReportID != secondManifest.ReportID || firstManifest.ReportID != decodedSecond.ReportID {
		t.Fatalf("timestamp changed report ID: %#v vs %#v", firstManifest, secondManifest)
	}
	if bytes.Equal(first.puts[1].body, second.puts[1].body) {
		t.Fatal("manifest body did not change when generated_at changed")
	}
	if !bytes.Equal(first.puts[0].body, second.puts[0].body) {
		t.Fatal("identical report produced different immutable bytes")
	}
}

func TestPublishCensusReportDifferentReportBytesChangeReportID(t *testing.T) {
	t.Parallel()

	first := new(recordingSink)
	firstReport := testCensusReport(t)
	firstReport.ImportReport.WarmSHA256 = testSHA256String("warm-a")
	firstManifest, err := publishCensusReport(context.Background(), first, time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), firstReport)
	if err != nil {
		t.Fatalf("publishCensusReport(first): %v", err)
	}

	second := new(recordingSink)
	secondReport := testCensusReport(t)
	secondReport.ImportReport.WarmSHA256 = testSHA256String("warm-b")
	secondManifest, err := publishCensusReport(context.Background(), second, time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), secondReport)
	if err != nil {
		t.Fatalf("publishCensusReport(second): %v", err)
	}

	if firstManifest.ReportID == secondManifest.ReportID {
		t.Fatalf("different report bytes reused report ID %q", firstManifest.ReportID)
	}
}

func TestPublishCensusReportDoesNotWritePointerAfterImmutableFailure(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{failAt: 1}
	err := publishCensusReportError(context.Background(), sink, time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), testCensusReport(t))
	if err == nil || !strings.Contains(err.Error(), "publish census report") {
		t.Fatalf("publishCensusReport error = %v", err)
	}
	for _, put := range sink.puts {
		if put.key == censusManifestKey {
			t.Fatal("manifest was attempted after immutable upload failure")
		}
	}
}

func TestPublishCensusReportPropagatesPointerFailure(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{failAt: 2}
	err := publishCensusReportError(context.Background(), sink, time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), testCensusReport(t))
	if err == nil || !strings.Contains(err.Error(), "publish census manifest") {
		t.Fatalf("publishCensusReport error = %v", err)
	}
	if len(sink.puts) != 2 {
		t.Fatalf("Put calls = %d, want 2", len(sink.puts))
	}
}

func TestParseCensusArgsRejectsMutuallyExclusiveWarmInputs(t *testing.T) {
	t.Parallel()

	_, err := parseCensusArgs([]string{"--seed-only", "--warm-file", "warm.ndjson"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("parseCensusArgs error = %v", err)
	}
}

func TestParseCensusArgsReturnsHelp(t *testing.T) {
	t.Parallel()

	_, err := parseCensusArgs([]string{"-h"}, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseCensusArgs error = %v, want %v", err, flag.ErrHelp)
	}
}

func TestParseCensusArgsRejectsWikidataDumpInputConflicts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{name: "explicit seed", args: []string{"--wikidata-dump", "dump.json", "--seed=data/seed"}},
		{name: "true seed only", args: []string{"--wikidata-dump", "dump.json", "--seed-only"}},
		{name: "nonempty warm file", args: []string{"--wikidata-dump", "dump.json", "--warm-file=warm.ndjson"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseCensusArgs(tc.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "--wikidata-dump is mutually exclusive") {
				t.Fatalf("parseCensusArgs error = %v, want Wikidata dump conflict", err)
			}
		})
	}
}

func TestParseCensusArgsAllowsInactiveInputsWithWikidataDump(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{name: "dump only", args: []string{"--wikidata-dump", "dump.json"}},
		{name: "false seed only", args: []string{"--wikidata-dump", "dump.json", "--seed-only=false"}},
		{name: "empty warm file", args: []string{"--wikidata-dump", "dump.json", "--warm-file="}},
		{name: "both inactive inputs", args: []string{"--wikidata-dump", "dump.json", "--seed-only=false", "--warm-file="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts, err := parseCensusArgs(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("parseCensusArgs: %v", err)
			}
			if !opts.wikidataDumpSet || opts.wikidataDump != "dump.json" {
				t.Fatalf("Wikidata dump options = %#v", opts)
			}
		})
	}
}

func TestRunCensusWithIOStreamsWikidataDumpFromPathAndStdin(t *testing.T) {
	fixturePath := "../../internal/ingest/testdata/wikidata-dump-mini.json"
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	missingAWSConfig := filepath.Join(t.TempDir(), "missing-aws-config")
	t.Setenv("AWS_PROFILE", "wikidata-dump-test-profile-does-not-exist")
	t.Setenv("AWS_CONFIG_FILE", missingAWSConfig)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missingAWSConfig)
	cases := []struct {
		name  string
		args  []string
		stdin io.Reader
	}{
		{name: "path", args: []string{"--wikidata-dump", fixturePath}, stdin: strings.NewReader("unused")},
		{name: "stdin", args: []string{"--wikidata-dump", "-"}, stdin: bytes.NewReader(body)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := runCensusWithIO(context.Background(), tc.args, tc.stdin, &stdout, &stderr); err != nil {
				t.Fatalf("runCensusWithIO: %v", err)
			}
			if got := stdout.String(); got != expectedWikidataDumpCoverageJSON {
				t.Fatalf("stdout mismatch\n got: %s\nwant: %s", got, expectedWikidataDumpCoverageJSON)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunCensusWithIORejectsInvalidWikidataDumpWithoutOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		args  []string
		stdin io.Reader
		want  string
	}{
		{
			name:  "missing path",
			args:  []string{"--wikidata-dump", filepath.Join(t.TempDir(), "missing.json")},
			stdin: strings.NewReader("unused"),
			want:  "open --wikidata-dump",
		},
		{
			name:  "malformed stdin",
			args:  []string{"--wikidata-dump", "-"},
			stdin: strings.NewReader(`[{"id":"Q1","type":"item"`),
			want:  `scan --wikidata-dump "-"`,
		},
		{
			name:  "explicit empty path",
			args:  []string{"--wikidata-dump="},
			stdin: strings.NewReader("unused"),
			want:  "--wikidata-dump requires a non-empty path or -",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			err := runCensusWithIO(context.Background(), tc.args, tc.stdin, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runCensusWithIO error = %v, want substring %q", err, tc.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunCensusWithIOReturnsWikidataDumpOutputError(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../internal/ingest/testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	sentinel := errors.New("write failed")
	err = runCensusWithIO(
		context.Background(),
		[]string{"--wikidata-dump", "-"},
		bytes.NewReader(body),
		censusErrorWriter{err: sentinel},
		io.Discard,
	)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "encode --wikidata-dump report") {
		t.Fatalf("runCensusWithIO error = %v, want wrapped writer error", err)
	}
}

func TestRunCensusWithRunnerSeedOnlyPublishesWarmSourceNone(t *testing.T) {
	t.Parallel()

	sink := new(recordingSink)
	called := false
	var stdout bytes.Buffer
	err := runCensusWithRunner(context.Background(), censusOptions{
		seedDir:  "../../data/seed",
		seedOnly: true,
	}, censusRunner{
		stdout: &stdout,
		now:    func() time.Time { return time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC) },
		sink:   sink,
		loadWarm: func(context.Context, censusOptions) ([]byte, ingest.WarmSource, error) {
			called = true
			return nil, ingest.WarmSourceNone, nil
		},
	})
	if err != nil {
		t.Fatalf("runCensusWithRunner: %v", err)
	}
	if !called {
		t.Fatal("seed-only run did not invoke the warm loader")
	}

	report := decodeCensusReport(t, sink.puts[0].body)
	if report.ImportReport.WarmSource != ingest.WarmSourceNone {
		t.Fatalf("WarmSource = %q, want %q", report.ImportReport.WarmSource, ingest.WarmSourceNone)
	}
	if report.ImportReport.WarmSHA256 != "" || report.ImportReport.Accepted.Warm != 0 {
		t.Fatalf("warm import report = %#v", report.ImportReport)
	}
	if strings.Contains(stdout.String(), "warm events:") {
		t.Fatalf("seed-only stdout unexpectedly mentioned warm merge: %q", stdout.String())
	}
}

func TestRunCensusWithRunnerWarmFilePublishesWarmFileSource(t *testing.T) {
	t.Parallel()

	warm, err := os.ReadFile("../../internal/ingest/testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	sink := new(recordingSink)
	var stdout bytes.Buffer
	err = runCensusWithRunner(context.Background(), censusOptions{
		seedDir:  "../../data/seed",
		warmFile: "warm.ndjson",
	}, censusRunner{
		stdout: &stdout,
		now:    func() time.Time { return time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC) },
		sink:   sink,
		loadWarm: func(context.Context, censusOptions) ([]byte, ingest.WarmSource, error) {
			return warm, ingest.WarmSourceWarmFile, nil
		},
	})
	if err != nil {
		t.Fatalf("runCensusWithRunner: %v", err)
	}

	report := decodeCensusReport(t, sink.puts[0].body)
	if report.ImportReport.WarmSource != ingest.WarmSourceWarmFile {
		t.Fatalf("WarmSource = %q, want %q", report.ImportReport.WarmSource, ingest.WarmSourceWarmFile)
	}
	if report.ImportReport.WarmSHA256 != testSHA256Bytes(warm) {
		t.Fatalf("WarmSHA256 = %q, want %q", report.ImportReport.WarmSHA256, testSHA256Bytes(warm))
	}
	if report.ImportReport.Accepted.Warm != 2 {
		t.Fatalf("Accepted.Warm = %d, want 2", report.ImportReport.Accepted.Warm)
	}
	if !strings.Contains(stdout.String(), "warm events: 2 merged, 0 deduped against seed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCensusWithRunnerRejectsNilSinkAndWarmLoader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		runner censusRunner
		want   string
	}{
		{name: "nil sink", runner: censusRunner{loadWarm: func(context.Context, censusOptions) ([]byte, ingest.WarmSource, error) {
			return nil, ingest.WarmSourceNone, nil
		}}, want: "nil sink"},
		{name: "nil warm loader", runner: censusRunner{sink: new(recordingSink)}, want: "nil warm loader"},
	}

	for _, tc := range cases {
		err := runCensusWithRunner(context.Background(), censusOptions{seedDir: "../../data/seed"}, tc.runner)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s error = %v", tc.name, err)
		}
	}
}

func TestRunCensusFailsLoudWhenDefaultWarmInputIsMissing(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err := runCensus(context.Background(), []string{"--seed", "../../data/seed"})
	if err == nil || !strings.Contains(err.Error(), "load default warm input "+warmEventsKey) {
		t.Fatalf("runCensus error = %v", err)
	}
	requests := server.requests()
	if !containsRequest(requests, "/wk-warm-test/"+warmEventsKey) {
		t.Fatalf("default warm object was not requested: %v", requests)
	}
	if containsRequest(requests, "/wk-warm-test/reports/census/") {
		t.Fatalf("publication attempted after missing warm input: %v", requests)
	}
}

func TestRunCensusDefaultWarmObjectPublishesToWarmBucket(t *testing.T) {
	warm, err := os.ReadFile("../../internal/ingest/testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	server := newBakeS3Server(warm)
	defer server.Close()
	server.installEnv(t)

	err = runCensus(context.Background(), []string{"--seed", "../../data/seed"})
	if err != nil {
		t.Fatalf("runCensus: %v", err)
	}
	requests := server.requests()
	if !containsRequest(requests, "/wk-warm-test/"+warmEventsKey) {
		t.Fatalf("default warm object was not requested: %v", requests)
	}
	if !containsRequest(requests, "/wk-warm-test/reports/census/") {
		t.Fatalf("census report was not published to wk-warm: %v", requests)
	}
	if !containsRequest(requests, "/wk-warm-test/"+censusManifestKey) {
		t.Fatalf("census manifest was not published: %v", requests)
	}
}

func TestRunCensusSeedOnlySkipsDefaultWarmObjectRead(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err := runCensus(context.Background(), []string{"--seed", "../../data/seed", "--seed-only"})
	if err != nil {
		t.Fatalf("runCensus: %v", err)
	}
	requests := server.requests()
	if containsRequest(requests, "/wk-warm-test/"+warmEventsKey) {
		t.Fatalf("seed-only run fetched default warm object: %v", requests)
	}
	if !containsRequest(requests, "/wk-warm-test/reports/census/") {
		t.Fatalf("seed-only run did not publish census objects: %v", requests)
	}
}

func TestRunCensusWarmFilePublishesWithoutDefaultWarmObjectRead(t *testing.T) {
	warmPath := filepath.Join(t.TempDir(), "warm.ndjson")
	warm, err := os.ReadFile("../../internal/ingest/testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	if err := os.WriteFile(warmPath, warm, 0o644); err != nil {
		t.Fatalf("write warm file: %v", err)
	}
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err = runCensus(context.Background(), []string{"--seed", "../../data/seed", "--warm-file", warmPath})
	if err != nil {
		t.Fatalf("runCensus: %v", err)
	}
	requests := server.requests()
	if containsRequest(requests, "/wk-warm-test/"+warmEventsKey) {
		t.Fatalf("--warm-file run fetched default warm object: %v", requests)
	}
	if !containsRequest(requests, "/wk-warm-test/reports/census/") {
		t.Fatalf("--warm-file run did not publish census objects: %v", requests)
	}
}

func TestRunCensusImmutableFailureSuppressesPointer(t *testing.T) {
	warm, err := os.ReadFile("../../internal/ingest/testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	server := newBakeS3Server(warm)
	server.failPutSubstring = "/wk-warm-test/reports/census/"
	defer server.Close()
	server.installEnv(t)

	err = runCensus(context.Background(), []string{"--seed", "../../data/seed"})
	if err == nil || !strings.Contains(err.Error(), "publish census report") {
		t.Fatalf("runCensus error = %v", err)
	}
	requests := server.requests()
	if containsRequest(requests, "/wk-warm-test/"+censusManifestKey) {
		t.Fatalf("manifest was attempted after immutable failure: %v", requests)
	}
}

func TestRunCensusPointerFailureReturnsError(t *testing.T) {
	warm, err := os.ReadFile("../../internal/ingest/testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	server := newBakeS3Server(warm)
	server.failPutSubstring = "/wk-warm-test/" + censusManifestKey
	defer server.Close()
	server.installEnv(t)

	err = runCensus(context.Background(), []string{"--seed", "../../data/seed"})
	if err == nil || !strings.Contains(err.Error(), "publish census manifest") {
		t.Fatalf("runCensus error = %v", err)
	}
	requests := server.requests()
	if !containsRequest(requests, "/wk-warm-test/reports/census/") {
		t.Fatalf("immutable report was not attempted before manifest failure: %v", requests)
	}
}

func TestUsageTextIncludesCensusModes(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"census --wikidata-dump <path|->",
		"read decoded Wikidata JSON",
		"report to stdout",
		"externally decompressed JSON",
		"mutually exclusive with seed/warm inputs",
		"census [--seed <dir>] [--seed-only | --warm-file <path>]",
		"BUCKET_WARM/" + warmEventsKey,
		"--seed-only / --warm-file input",
	} {
		if !strings.Contains(usageText, want) {
			t.Fatalf("usageText missing %q", want)
		}
	}
}

func decodeCensusManifest(t *testing.T, body []byte) censusManifest {
	t.Helper()

	var manifest censusManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode census manifest: %v", err)
	}
	return manifest
}

func decodeCensusReport(t *testing.T, body []byte) ingest.CensusReport {
	t.Helper()

	var report ingest.CensusReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode census report: %v", err)
	}
	return report
}

func testCensusReport(t *testing.T) ingest.CensusReport {
	t.Helper()

	report, err := ingest.BuildCensusReport(&ingest.Result{
		SeedVersion:     "seed-census",
		SeedInputSHA256: testSHA256String("seed"),
		Entities:        nil,
	}, ingest.WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport: %v", err)
	}
	return report
}

func publishCensusReportError(ctx context.Context, sink bake.Sink, generatedAt time.Time, report ingest.CensusReport) error {
	_, err := publishCensusReport(ctx, sink, generatedAt, report)
	return err
}

func testSHA256Bytes(body []byte) string {
	return fmt.Sprintf("%x", sha256Bytes(body))
}

func sha256Bytes(body []byte) [32]byte {
	return sha256.Sum256(body)
}
