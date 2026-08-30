package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The three fixtures hold the same dump, so the decoded bytes must match
// exactly whichever container they arrive in.
func TestOpenWikidataDumpStreamDecodesEveryContainer(t *testing.T) {
	t.Parallel()

	plain, err := os.ReadFile("testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	tests := []struct {
		path string
		want DumpCompression
	}{
		{"testdata/wikidata-dump-mini.json", DumpCompressionNone},
		{"testdata/wikidata-dump-mini.json.gz", DumpCompressionGzip},
		{"testdata/wikidata-dump-mini.json.bz2", DumpCompressionBzip2},
	}
	for _, tt := range tests {
		t.Run(string(tt.want), func(t *testing.T) {
			t.Parallel()

			file, err := os.Open(tt.path)
			if err != nil {
				t.Fatalf("open %s: %v", tt.path, err)
			}
			defer file.Close()

			stream, compression, err := OpenWikidataDumpStream(file)
			if err != nil {
				t.Fatalf("OpenWikidataDumpStream: %v", err)
			}
			if compression != tt.want {
				t.Fatalf("compression = %q, want %q", compression, tt.want)
			}
			decoded, err := io.ReadAll(stream)
			if err != nil {
				t.Fatalf("read decoded stream: %v", err)
			}
			if !bytes.Equal(decoded, plain) {
				t.Fatalf("decoded %d bytes, want the %d-byte plain fixture", len(decoded), len(plain))
			}
		})
	}
}

// The coverage report must be identical whichever container the same dump
// arrives in, apart from the compression field and the digest of the input
// artifact itself.
func TestBuildWikidataDumpCoverageReportReadsCompressedDumps(t *testing.T) {
	t.Parallel()

	plainReport := coverageReportForFile(t, "testdata/wikidata-dump-mini.json")
	if plainReport.Compression != DumpCompressionNone {
		t.Fatalf("plain compression = %q", plainReport.Compression)
	}

	for _, path := range []string{"testdata/wikidata-dump-mini.json.gz", "testdata/wikidata-dump-mini.json.bz2"} {
		got := coverageReportForFile(t, path)
		if got.InputSHA256 != fileSHA256(t, path) {
			t.Fatalf("%s: input_sha256 = %q, want the digest of the compressed artifact", path, got.InputSHA256)
		}
		normalized := got
		normalized.Compression = plainReport.Compression
		normalized.InputSHA256 = plainReport.InputSHA256
		if !reflect.DeepEqual(normalized, plainReport) {
			t.Fatalf("%s: report differs from the plain one\n got: %#v\nwant: %#v", path, normalized, plainReport)
		}
	}
}

func TestOpenWikidataDumpStreamFailsLoudly(t *testing.T) {
	t.Parallel()

	if _, _, err := OpenWikidataDumpStream(nil); err == nil || !strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("OpenWikidataDumpStream(nil) error = %v", err)
	}
	// A gzip magic with a broken header must not be mistaken for plain JSON.
	if _, _, err := OpenWikidataDumpStream(bytes.NewReader([]byte{0x1f, 0x8b, 0x00, 0x00})); err == nil ||
		!strings.Contains(err.Error(), "gzip") {
		t.Fatalf("OpenWikidataDumpStream(bad gzip) error = %v", err)
	}
}

func coverageReportForFile(t *testing.T, path string) WikidataDumpCoverageReport {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	report, err := BuildWikidataDumpCoverageReport(file)
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport(%s): %v", path, err)
	}
	return report
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
