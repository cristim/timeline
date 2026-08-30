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

	firstReport, err := BuildImportReport(first, WarmSourceWarmFile, testSHA256("warm-a"), nil)
	if err != nil {
		t.Fatalf("BuildImportReport(first): %v", err)
	}
	secondReport, err := BuildImportReport(second, WarmSourceWarmFile, testSHA256("warm-a"), nil)
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
		if _, err := BuildImportReport(res, tc.warmSource, tc.warmSHA256, nil); err == nil {
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
		}, WarmSourceNone, "", nil)
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
		if _, err := BuildImportReport(tc.res, tc.warmSource, tc.warmSHA256, nil); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestBuildImportReportDifferentSeedInputDigestChangesJSON(t *testing.T) {
	t.Parallel()

	first, err := BuildImportReport(&Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256("seed-a"),
	}, WarmSourceNone, "", nil)
	if err != nil {
		t.Fatalf("BuildImportReport(first): %v", err)
	}
	second, err := BuildImportReport(&Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256("seed-b"),
	}, WarmSourceNone, "", nil)
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

func TestBuildImportReportNormalizesOHMSummary(t *testing.T) {
	t.Parallel()

	firstOHM := testOHMSummary()
	secondOHM := testOHMSummary()
	secondOHM.Licenses[0], secondOHM.Licenses[1] = secondOHM.Licenses[1], secondOHM.Licenses[0]
	secondOHM.LicenseExceptions[0], secondOHM.LicenseExceptions[1] = secondOHM.LicenseExceptions[1], secondOHM.LicenseExceptions[0]
	result := &Result{SeedVersion: "seed-ohm", SeedInputSHA256: testSHA256("seed-ohm")}

	first, err := BuildImportReport(result, WarmSourceNone, "", firstOHM)
	if err != nil {
		t.Fatalf("BuildImportReport(first): %v", err)
	}
	second, err := BuildImportReport(result, WarmSourceNone, "", secondOHM)
	if err != nil {
		t.Fatalf("BuildImportReport(second): %v", err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("OHM order changed report JSON\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
	if first.OHM == firstOHM {
		t.Fatal("report retained the caller-owned OHM summary")
	}
	if firstOHM.Licenses[0].License != "ODbL-1.0" {
		t.Fatalf("caller summary was mutated: %#v", firstOHM.Licenses)
	}
}

func TestBuildImportReportRejectsInvalidOHMSummary(t *testing.T) {
	t.Parallel()

	result := &Result{SeedVersion: "seed-ohm", SeedInputSHA256: testSHA256("seed-ohm")}
	cases := []struct {
		name   string
		mutate func(*OHMImportSummary)
	}{
		{name: "source", mutate: func(s *OHMImportSummary) { s.Source = "other" }},
		{name: "digest", mutate: func(s *OHMImportSummary) { s.InputSHA256 = "bad" }},
		{name: "retrieval time", mutate: func(s *OHMImportSummary) { s.RetrievedAt = "yesterday" }},
		{name: "counters", mutate: func(s *OHMImportSummary) { s.Accepted++ }},
		{name: "license total", mutate: func(s *OHMImportSummary) { s.Licenses[0].Count++ }},
		{name: "duplicate license", mutate: func(s *OHMImportSummary) { s.Licenses[1].License = s.Licenses[0].License }},
		{name: "duplicate source id", mutate: func(s *OHMImportSummary) { s.LicenseExceptions[1].SourceID = s.LicenseExceptions[0].SourceID }},
		{name: "uncounted exception license", mutate: func(s *OHMImportSummary) { s.LicenseExceptions[0].License = "CC-BY-4.0" }},
		{name: "action", mutate: func(s *OHMImportSummary) { s.LicenseExceptions[0].Action = LicenseAction("ignored") }},
		{name: "excluded reason", mutate: func(s *OHMImportSummary) { s.LicenseExceptions[0].Reason = "" }},
		{name: "excluded exception count", mutate: func(s *OHMImportSummary) { s.LicenseExceptions = s.LicenseExceptions[1:] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := testOHMSummary()
			tc.mutate(summary)
			if _, err := BuildImportReport(result, WarmSourceNone, "", summary); err == nil {
				t.Fatal("expected invalid OHM summary to fail")
			}
		})
	}
}

func testOHMSummary() *OHMImportSummary {
	return &OHMImportSummary{
		Source: "OpenHistoricalMap", InputSHA256: testSHA256("ohm"), RetrievedAt: "2026-08-30T06:51:56Z",
		Parsed: 2, Accepted: 1, Excluded: 1,
		Licenses: []LicenseCount{{License: "ODbL-1.0", Count: 1}, {License: "CC0-1.0", Count: 1}},
		LicenseExceptions: []LicenseException{
			{SourceID: "relation/2@1", License: "ODbL-1.0", Attribution: "OHM", Action: LicenseExcluded, Reason: "unsupported"},
			{SourceID: "relation/1@1", License: "CC0-1.0", Attribution: "OHM", Action: LicenseAccepted},
		},
	}
}

func testSHA256(input string) string {
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", sum[:])
}
