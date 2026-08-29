package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func TestBuildImportReportDeterministicReasonGroupingAndJSON(t *testing.T) {
	t.Parallel()

	first := &Result{
		SeedVersion:           "seed-deadbeef",
		SeedInputSHA256:       testSHA256("seed-a"),
		SeedParsed:            3,
		SeedAccepted:          1,
		WarmParsed:            4,
		WarmAccepted:          1,
		WarmDuplicatesSkipped: 1,
		Rejects: []Reject{
			{Source: RejectSourceWarm, File: "warm:events", Line: 9, Reason: "bad warm"},
			{Source: RejectSourceSeed, File: "seed.ndjson", Line: 4, Reason: "bad seed"},
			{Source: RejectSourceWarm, File: "warm:events", Line: 5, Reason: "bad warm"},
			{Source: RejectSourceSeed, File: "seed.ndjson", Line: 7, Reason: "bad seed"},
		},
	}
	second := &Result{
		SeedVersion:           first.SeedVersion,
		SeedInputSHA256:       first.SeedInputSHA256,
		SeedParsed:            first.SeedParsed,
		SeedAccepted:          first.SeedAccepted,
		WarmParsed:            first.WarmParsed,
		WarmAccepted:          first.WarmAccepted,
		WarmDuplicatesSkipped: first.WarmDuplicatesSkipped,
		Rejects: []Reject{
			first.Rejects[2],
			first.Rejects[0],
			first.Rejects[3],
			first.Rejects[1],
		},
	}

	firstReport, err := BuildImportReport(first, WarmSourceWarmFile, testSHA256("warm-a"))
	if err != nil {
		t.Fatalf("BuildImportReport(first): %v", err)
	}
	secondReport, err := BuildImportReport(second, WarmSourceWarmFile, testSHA256("warm-a"))
	if err != nil {
		t.Fatalf("BuildImportReport(second): %v", err)
	}

	if firstReport.SchemaVersion != ImportReportSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", firstReport.SchemaVersion, ImportReportSchemaVersion)
	}
	if firstReport.Parsed != (ImportCounts{Seed: 3, Warm: 4, Total: 7}) {
		t.Fatalf("Parsed = %#v, want 3/4/7", firstReport.Parsed)
	}
	if firstReport.Accepted != (ImportCounts{Seed: 1, Warm: 1, Total: 2}) {
		t.Fatalf("Accepted = %#v, want 1/1/2", firstReport.Accepted)
	}
	if firstReport.Rejected != (ImportCounts{Seed: 2, Warm: 2, Total: 4}) {
		t.Fatalf("Rejected = %#v, want 2/2/4", firstReport.Rejected)
	}
	wantReasons := []RejectReasonCount{
		{Source: RejectSourceSeed, Reason: "bad seed", Count: 2},
		{Source: RejectSourceWarm, Reason: "bad warm", Count: 2},
	}
	if len(firstReport.RejectReasons) != len(wantReasons) {
		t.Fatalf("RejectReasons len = %d, want %d", len(firstReport.RejectReasons), len(wantReasons))
	}
	for i := range wantReasons {
		if firstReport.RejectReasons[i] != wantReasons[i] {
			t.Fatalf("RejectReasons[%d] = %#v, want %#v", i, firstReport.RejectReasons[i], wantReasons[i])
		}
	}

	firstJSON, err := json.Marshal(firstReport)
	if err != nil {
		t.Fatalf("marshal first report: %v", err)
	}
	secondJSON, err := json.Marshal(secondReport)
	if err != nil {
		t.Fatalf("marshal second report: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("deterministic JSON mismatch\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestBuildImportReportValidatesWarmSourceAndDigest(t *testing.T) {
	t.Parallel()

	res := &Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256("seed-a"),
	}

	cases := []struct {
		name       string
		warmSource WarmSource
		warmSHA256 string
	}{
		{name: "unknown source", warmSource: WarmSource("mystery"), warmSHA256: ""},
		{name: "none with digest", warmSource: WarmSourceNone, warmSHA256: testSHA256("warm-a")},
		{name: "warm-file missing digest", warmSource: WarmSourceWarmFile, warmSHA256: ""},
		{name: "wikidata digest not hex", warmSource: WarmSourceWikidataEvents, warmSHA256: "xyz"},
	}
	for _, tc := range cases {
		if _, err := BuildImportReport(res, tc.warmSource, tc.warmSHA256); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestBuildImportReportRejectsInvalidSeedInputSHA256(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		seedInputSHA256 string
	}{
		{name: "empty", seedInputSHA256: ""},
		{name: "non-hex", seedInputSHA256: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{name: "wrong length", seedInputSHA256: "abcd"},
	}

	for _, tc := range cases {
		_, err := BuildImportReport(&Result{
			SeedVersion:     "seed-deadbeef",
			SeedInputSHA256: tc.seedInputSHA256,
		}, WarmSourceNone, "")
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestBuildImportReportRejectsInvalidCounterStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		res        *Result
		warmSource WarmSource
		warmSHA256 string
	}{
		{
			name: "seed accepted plus rejects exceeds parsed",
			res: &Result{
				SeedVersion:     "seed-deadbeef",
				SeedInputSHA256: testSHA256("seed-a"),
				SeedParsed:      1,
				SeedAccepted:    1,
				Rejects:         []Reject{{Source: RejectSourceSeed, File: "seed", Line: 1, Reason: "bad"}},
			},
			warmSource: WarmSourceNone,
		},
		{
			name: "warm accounting mismatch",
			res: &Result{
				SeedVersion:           "seed-deadbeef",
				SeedInputSHA256:       testSHA256("seed-a"),
				WarmParsed:            2,
				WarmAccepted:          1,
				WarmDuplicatesSkipped: 0,
			},
			warmSource: WarmSourceWarmFile,
			warmSHA256: testSHA256("warm-a"),
		},
		{
			name: "warm reject without warm source",
			res: &Result{
				SeedVersion:     "seed-deadbeef",
				SeedInputSHA256: testSHA256("seed-a"),
				WarmParsed:      1,
				Rejects:         []Reject{{Source: RejectSourceWarm, File: "warm", Line: 1, Reason: "bad"}},
			},
			warmSource: WarmSourceNone,
		},
		{
			name: "negative counter",
			res: &Result{
				SeedVersion:     "seed-deadbeef",
				SeedInputSHA256: testSHA256("seed-a"),
				SeedParsed:      -1,
			},
			warmSource: WarmSourceNone,
		},
		{
			name: "unknown reject source",
			res: &Result{
				SeedVersion:     "seed-deadbeef",
				SeedInputSHA256: testSHA256("seed-a"),
				SeedParsed:      1,
				Rejects:         []Reject{{Source: RejectSource("mystery"), File: "seed", Line: 1, Reason: "bad"}},
			},
			warmSource: WarmSourceNone,
		},
	}

	for _, tc := range cases {
		if _, err := BuildImportReport(tc.res, tc.warmSource, tc.warmSHA256); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestBuildImportReportDifferentSeedInputDigestChangesJSON(t *testing.T) {
	t.Parallel()

	first, err := BuildImportReport(&Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256("seed-a"),
	}, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildImportReport(first): %v", err)
	}
	second, err := BuildImportReport(&Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256("seed-b"),
	}, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildImportReport(second): %v", err)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first report: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second report: %v", err)
	}
	if bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("different seed input digests produced identical JSON: %s", firstJSON)
	}
}

func testSHA256(input string) string {
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", sum[:])
}
