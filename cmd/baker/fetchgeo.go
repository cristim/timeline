package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"wk/internal/ingest"
)

const (
	basemapFetchTimeout    = 15 * time.Minute
	basemapCommandWaitTime = 5 * time.Second
)

type basemapCommand struct {
	Executable   string
	Arguments    []string
	EnvOverrides []string
}

type basemapCommandRunner func(context.Context, basemapCommand) ([]byte, error)

// Fetched map inputs live in data/geo rather than S3, and their output is
// gitignored. CI restores them from a cache keyed on geo-fingerprint.

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

func runFetchBasemap(ctx context.Context, args []string) error {
	return runFetchBasemapWith(ctx, args, ingest.ProductionBasemap, runBasemapCommand)
}

func runFetchBasemapWith(ctx context.Context, args []string, spec ingest.BasemapSpec, runner basemapCommandRunner) error {
	return runFetchBasemapWithCleanup(ctx, args, spec, runner, os.RemoveAll)
}

func runFetchBasemapWithCleanup(ctx context.Context, args []string, spec ingest.BasemapSpec, runner basemapCommandRunner, cleanup func(string) error) (returnErr error) {
	fs := flag.NewFlagSet("fetch-basemap", flag.ContinueOnError)
	outDir := fs.String("out", "data/geo/basemap", "where to write the basemap archive")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if spec.GoToolchain == "" {
		return errors.New("basemap generating toolchain is required")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("create basemap output directory %s: %w", *outDir, err)
	}
	tempDir, err := os.MkdirTemp(*outDir, ".fetch-basemap-")
	if err != nil {
		return fmt.Errorf("create temporary basemap directory: %w", err)
	}
	defer func() {
		if err := cleanup(tempDir); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary basemap directory %s: %w", tempDir, err))
		}
	}()

	tempOutput := filepath.Join(tempDir, spec.Filename)
	commandCtx, cancel := context.WithTimeout(ctx, basemapFetchTimeout)
	defer cancel()
	commandArgs := []string{
		"run", spec.Tool, "extract", spec.Source, tempOutput,
		"--bbox=" + spec.BBox,
		fmt.Sprintf("--maxzoom=%d", spec.MaxZoom),
		fmt.Sprintf("--overfetch=%d", spec.Overfetch),
	}
	output, err := runner(commandCtx, basemapCommand{
		Executable:   "go",
		Arguments:    commandArgs,
		EnvOverrides: []string{"GOTOOLCHAIN=" + spec.GoToolchain},
	})
	if err != nil {
		stderr := strings.TrimSpace(string(output))
		if ctxErr := commandCtx.Err(); ctxErr != nil {
			return fmt.Errorf("run %s: %w (%v): %s", spec.Tool, ctxErr, err, stderr)
		}
		return fmt.Errorf("run %s: %w: %s", spec.Tool, err, stderr)
	}
	if err := commandCtx.Err(); err != nil {
		return fmt.Errorf("run %s: %w", spec.Tool, err)
	}
	if _, err := ingest.VerifyBasemap(tempDir, spec); err != nil {
		return err
	}
	finalPath := filepath.Join(*outDir, spec.Filename)
	if err := os.Rename(tempOutput, finalPath); err != nil {
		return fmt.Errorf("publish basemap %s: %w", finalPath, err)
	}
	fmt.Printf("wrote basemap to %s (%.1f MB)\n", finalPath, float64(spec.Size)/(1<<20))
	return nil
}

func runBasemapCommand(ctx context.Context, request basemapCommand) ([]byte, error) {
	cmd := exec.CommandContext(ctx, request.Executable, request.Arguments...)
	cmd.Env = append(cmd.Environ(), request.EnvOverrides...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = basemapCommandWaitTime
	return cmd.CombinedOutput()
}

// runGeoFingerprint prints a short hash of the generated-file restore contract.
func runGeoFingerprint() error {
	fmt.Println(geoFingerprint(ingest.ProductionBasemap)[:16])
	return nil
}

func geoFingerprint(basemap ingest.BasemapSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "borders=%s\n", ingest.BordersCommit)
	fmt.Fprintf(h, "paleo=%s\n", ingest.PaleoModel)
	fmt.Fprintf(h, "slices=%v\n", ingest.PaleoSlices)
	fmt.Fprintf(h, "basemap.source=%s\n", basemap.Source)
	fmt.Fprintf(h, "basemap.tool=%s\n", basemap.Tool)
	fmt.Fprintf(h, "basemap.gotoolchain=%s\n", basemap.GoToolchain)
	fmt.Fprintf(h, "basemap.bbox=%s\n", basemap.BBox)
	fmt.Fprintf(h, "basemap.maxzoom=%d\n", basemap.MaxZoom)
	fmt.Fprintf(h, "basemap.overfetch=%d\n", basemap.Overfetch)
	fmt.Fprintf(h, "basemap.filename=%s\n", basemap.Filename)
	fmt.Fprintf(h, "basemap.size=%d\n", basemap.Size)
	fmt.Fprintf(h, "basemap.sha256=%s\n", basemap.SHA256)
	return hex.EncodeToString(h.Sum(nil))
}

// runGeoVerify proves every fetched input is whole before a bake trusts it.
func runGeoVerify(args []string) error {
	fs := flag.NewFlagSet("geo-verify", flag.ContinueOnError)
	geoDir := fs.String("geo", "data/geo", "curated geometry directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var political ingest.AreaCoverage
	for _, layer := range []string{"borders", "paleo"} {
		c, err := ingest.VerifyAreaLayer(filepath.Join(*geoDir, layer))
		if err != nil {
			return err
		}
		fmt.Printf("%-8s %3d slices, contiguous %s .. %s\n",
			layer, c.Slices, ingest.FormatYear(c.TFrom), ingest.FormatYear(c.TTo))
		if layer == "borders" {
			political = c
		}
	}
	ohm, summary, err := ingest.VerifyOHM(filepath.Join(*geoDir, "ohm"), political.TTo)
	if err != nil {
		return err
	}
	fmt.Printf("%-8s %3d snapshots, %s .. %s; %d source relations accepted, %d excluded\n",
		"ohm", ohm.Slices, ingest.FormatYear(ohm.TFrom), ingest.FormatYear(ohm.TTo),
		summary.Accepted, summary.Excluded)
	body, err := ingest.VerifyBasemap(filepath.Join(*geoDir, "basemap"), ingest.ProductionBasemap)
	if err != nil {
		return err
	}
	fmt.Printf("%-8s %s, %d bytes, sha256 %s\n", "basemap", ingest.ProductionBasemap.Filename,
		len(body), ingest.ProductionBasemap.SHA256)
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
