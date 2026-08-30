package bake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTippecanoeCompilerUsesStableRelativeCommandAndCanonicalDescription(t *testing.T) {
	t.Parallel()

	var workDir, command string
	var args []string
	compiler := &TippecanoeCompiler{run: func(_ context.Context, dir, name string, commandArgs ...string) ([]byte, error) {
		workDir, command, args = dir, name, append([]string(nil), commandArgs...)
		input, err := os.ReadFile(filepath.Join(dir, "layer.geojson"))
		if err != nil {
			return nil, err
		}
		if string(input) != `{"type":"FeatureCollection","features":[]}` {
			t.Fatalf("input = %s", input)
		}
		return nil, os.WriteFile(filepath.Join(dir, "layer.pmtiles"), []byte("archive"), 0o644)
	}}
	request := LayerCompileRequest{
		Layer: BordersLayer, Year: 1942, TFrom: 1939, TTo: 1945,
		Label: "Axis maximum", Source: "atlas", Attribution: BordersAttribution,
		GeoJSON: []byte(`{"type":"FeatureCollection","features":[]}`),
	}
	body, err := compiler.Compile(context.Background(), request)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if string(body) != "archive" {
		t.Fatalf("body = %q", body)
	}
	wantArgs := []string{
		"--force", "--name=areas", "--layer=areas", "--minimum-zoom=0", "--maximum-zoom=6",
		"--no-feature-limit", "--no-tile-size-limit", "--detect-shared-borders", "--quiet",
		"--attribution=" + BordersAttribution,
		`--description={"layer":"borders","year":1942,"t_from":1939,"t_to":1945,"label":"Axis maximum","source":"atlas"}`,
		"--output=layer.pmtiles", "layer.geojson",
	}
	if command != "tippecanoe" || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("command = %q %#v, want tippecanoe %#v", command, args, wantArgs)
	}
	if filepath.Base(workDir) == "." || workDir == "" {
		t.Fatalf("work directory = %q", workDir)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("temporary directory still exists: %v", err)
	}
}

func TestTippecanoeCompilerReportsToolOutput(t *testing.T) {
	t.Parallel()
	compiler := &TippecanoeCompiler{run: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte("bad geometry"), errors.New("exit status 1")
	}}
	_, err := compiler.Compile(context.Background(), validCompileRequest())
	if err == nil || !strings.Contains(err.Error(), "bad geometry") || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("Compile error = %v", err)
	}
}

func TestTippecanoeCompilerRejectsMissingAndEmptyOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  commandRunner
		want string
	}{
		{name: "missing", run: func(context.Context, string, string, ...string) ([]byte, error) { return nil, nil }, want: "read layer.pmtiles"},
		{name: "empty", run: func(_ context.Context, dir, _ string, _ ...string) ([]byte, error) {
			return nil, os.WriteFile(filepath.Join(dir, "layer.pmtiles"), nil, 0o644)
		}, want: "empty PMTiles output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler := &TippecanoeCompiler{run: test.run}
			_, err := compiler.Compile(context.Background(), validCompileRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile error = %v", err)
			}
		})
	}
}

func TestTippecanoeCompilerPropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	compiler := &TippecanoeCompiler{run: func(ctx context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		return nil, ctx.Err()
	}}
	_, err := compiler.Compile(ctx, validCompileRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compile error = %v", err)
	}
}

func validCompileRequest() LayerCompileRequest {
	return LayerCompileRequest{
		Layer: BordersLayer, Year: 1942, TFrom: 1939, TTo: 1945,
		Label: "Axis maximum", Source: "atlas", Attribution: BordersAttribution,
		GeoJSON: []byte(`{"type":"FeatureCollection","features":[]}`),
	}
}
