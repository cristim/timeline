package bake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	tippecanoeInput  = "layer.geojson"
	tippecanoeOutput = "layer.pmtiles"
)

type commandRunner func(context.Context, string, string, ...string) ([]byte, error)

// TippecanoeCompiler turns one canonical GeoJSON area layer into a PMTiles v3 archive.
type TippecanoeCompiler struct {
	run commandRunner
}

func (c *TippecanoeCompiler) Compile(ctx context.Context, request LayerCompileRequest) ([]byte, error) {
	dir, err := os.MkdirTemp("", "world-knowledge-tippecanoe-")
	if err != nil {
		return nil, fmt.Errorf("create tippecanoe directory: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, tippecanoeInput), request.GeoJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", tippecanoeInput, err)
	}
	description, err := layerDescription(request)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--force",
		"--name=areas",
		"--layer=areas",
		"--minimum-zoom=0",
		"--maximum-zoom=6",
		"--no-feature-limit",
		"--no-tile-size-limit",
		"--detect-shared-borders",
		"--quiet",
		"--attribution=" + request.Attribution,
		"--description=" + string(description),
		"--output=" + tippecanoeOutput,
		tippecanoeInput,
	}
	runner := c.run
	if runner == nil {
		runner = runCommand
	}
	output, err := runner(ctx, dir, "tippecanoe", args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("run tippecanoe: %w (%v): %s", ctxErr, err, strings.TrimSpace(string(output)))
		}
		return nil, fmt.Errorf("run tippecanoe: %w: %s", err, strings.TrimSpace(string(output)))
	}
	body, err := os.ReadFile(filepath.Join(dir, tippecanoeOutput))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tippecanoeOutput, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("empty PMTiles output")
	}
	return body, nil
}

func layerDescription(request LayerCompileRequest) ([]byte, error) {
	description := layerDocProps{
		Layer: request.Layer, Year: request.Year, TFrom: request.TFrom,
		TTo: request.TTo, Label: request.Label, Source: request.Source,
	}
	body, err := json.Marshal(description)
	if err != nil {
		return nil, fmt.Errorf("encode PMTiles description: %w", err)
	}
	return body, nil
}

func runCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
