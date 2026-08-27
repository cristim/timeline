package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wk/internal/ingest"
)

// fetch-borders and fetch-paleo write into data/geo rather than S3, and their
// output is gitignored: the layers are derived from pinned upstreams, so CI
// rebuilds them behind a cache keyed on geo-fingerprint instead of the repo
// carrying ~23 MB of data it could regenerate.

func runFetchBorders(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch-borders", flag.ContinueOnError)
	outDir := fs.String("out", "data/geo/borders", "where to write the year slices")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli := &http.Client{Timeout: 180 * time.Second}
	slices, err := ingest.FetchBorders(ctx, cli, func(msg string) { fmt.Println("  " + msg) })
	if err != nil {
		return err
	}
	total, err := writeSlices(*outDir, sliceFiles(slices))
	if err != nil {
		return err
	}
	fmt.Printf("wrote %d border slices to %s (%.1f MB total)\n",
		len(slices), *outDir, float64(total)/(1<<20))
	return nil
}

func runFetchPaleo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch-paleo", flag.ContinueOnError)
	outDir := fs.String("out", "data/geo/paleo", "where to write the reconstruction slices")
	bordersDir := fs.String("borders", "data/geo/borders", "political slices, whose earliest year bounds this layer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The two layers must tile, not overlap, so the political layer's own
	// earliest year decides where deep time stops - not a second constant that
	// could drift away from it.
	politicalStart, err := ingest.EarliestBorderYear(*bordersDir)
	if err != nil {
		return err
	}
	fmt.Printf("political coverage starts at %s; deep time runs up to the year before\n",
		ingest.FormatYear(politicalStart))

	cli := &http.Client{Timeout: 300 * time.Second}
	slices, err := ingest.FetchPaleo(ctx, cli, politicalStart, func(msg string) { fmt.Println("  " + msg) })
	if err != nil {
		return err
	}
	files := make([]sliceFile, len(slices))
	for i, s := range slices {
		files[i] = sliceFile{Year: s.Year, Body: s.Body}
	}
	total, err := writeSlices(*outDir, files)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %d paleo slices to %s (%.1f MB total)\n",
		len(slices), *outDir, float64(total)/(1<<20))
	return nil
}

// runGeoFingerprint prints a short hash of everything that decides what the
// fetched layers contain. CI keys its cache on it, so bumping the pinned
// upstream commit, the plate model or the slice list invalidates the cache
// automatically instead of relying on someone remembering to bump a key.
func runGeoFingerprint() error {
	h := sha256.New()
	fmt.Fprintf(h, "borders=%s\n", ingest.BordersCommit)
	fmt.Fprintf(h, "paleo=%s\n", ingest.PaleoModel)
	fmt.Fprintf(h, "slices=%v\n", ingest.PaleoSlices)
	fmt.Println(hex.EncodeToString(h.Sum(nil))[:16])
	return nil
}

// runGeoVerify proves both fetched layers are whole before anything builds on
// them: loading rejects a layer whose coverage windows do not tile, so a
// partial or stale fetch fails here rather than deploying an atlas with holes.
func runGeoVerify(args []string) error {
	fs := flag.NewFlagSet("geo-verify", flag.ContinueOnError)
	geoDir := fs.String("geo", "data/geo", "curated geometry directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, layer := range []string{"borders", "paleo"} {
		c, err := ingest.VerifyAreaLayer(filepath.Join(*geoDir, layer))
		if err != nil {
			return err
		}
		fmt.Printf("%-8s %3d slices, contiguous %s .. %s\n",
			layer, c.Slices, ingest.FormatYear(c.TFrom), ingest.FormatYear(c.TTo))
	}
	return nil
}

type sliceFile struct {
	Year int
	Body []byte
}

func sliceFiles(slices []ingest.BorderSlice) []sliceFile {
	out := make([]sliceFile, len(slices))
	for i, s := range slices {
		out[i] = sliceFile{Year: s.Year, Body: s.Body}
	}
	return out
}

func writeSlices(dir string, files []sliceFile) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	total := 0
	for _, f := range files {
		path := filepath.Join(dir, fmt.Sprintf("%d.geojson", f.Year))
		if err := os.WriteFile(path, f.Body, 0o644); err != nil {
			return 0, err
		}
		total += len(f.Body)
	}
	return total, nil
}
