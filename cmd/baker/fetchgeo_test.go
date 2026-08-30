package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"wk/internal/ingest"
)

func TestRunFetchBasemapCreatesOutputAndUsesExactCommand(t *testing.T) {
	t.Parallel()
	spec, body := fetchTestBasemapSpec()
	outDir := filepath.Join(t.TempDir(), "absent", "basemap")
	var gotCommand basemapCommand
	calls := 0
	runner := func(ctx context.Context, request basemapCommand) ([]byte, error) {
		calls++
		gotCommand = basemapCommand{
			Executable:   request.Executable,
			Arguments:    append([]string(nil), request.Arguments...),
			EnvOverrides: append([]string(nil), request.EnvOverrides...),
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("command context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < basemapFetchTimeout-time.Second || remaining > basemapFetchTimeout+time.Second {
			t.Fatalf("command deadline remaining = %s, want %s", remaining, basemapFetchTimeout)
		}
		return nil, os.WriteFile(request.Arguments[4], body, 0o644)
	}

	err := runFetchBasemapWith(context.Background(), []string{"--out", outDir}, spec, runner)
	if err != nil {
		t.Fatalf("runFetchBasemapWith: %v", err)
	}
	wantCommand := basemapCommand{
		Executable: "go",
		Arguments: []string{
			"run", spec.Tool, "extract", spec.Source, filepath.Join(outDir, ".temporary", spec.Filename),
			"--bbox=" + spec.BBox, "--maxzoom=2", "--overfetch=0",
		},
		EnvOverrides: []string{"GOTOOLCHAIN=" + spec.GoToolchain},
	}
	if calls != 1 || len(gotCommand.Arguments) != len(wantCommand.Arguments) {
		t.Fatalf("command calls/request = %d %#v", calls, gotCommand)
	}
	wantCommand.Arguments[4] = gotCommand.Arguments[4]
	if !reflect.DeepEqual(gotCommand, wantCommand) {
		t.Fatalf("command = %#v, want %#v", gotCommand, wantCommand)
	}
	tempParent := filepath.Dir(gotCommand.Arguments[4])
	if filepath.Dir(tempParent) != outDir || !strings.HasPrefix(filepath.Base(tempParent), ".fetch-basemap-") {
		t.Fatalf("temporary output = %q, want child of %q", gotCommand.Arguments[4], outDir)
	}
	if filepath.Base(gotCommand.Arguments[4]) != spec.Filename {
		t.Fatalf("temporary output filename = %q, want %q", filepath.Base(gotCommand.Arguments[4]), spec.Filename)
	}
	got, err := os.ReadFile(filepath.Join(outDir, spec.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("final body = %q, want %q", got, body)
	}
	assertBasemapDirectory(t, outDir, spec.Filename)
}

func TestRunFetchBasemapRejectsMissingGeneratingToolchain(t *testing.T) {
	t.Parallel()
	spec, _ := fetchTestBasemapSpec()
	spec.GoToolchain = ""
	called := false
	runner := func(context.Context, basemapCommand) ([]byte, error) {
		called = true
		return nil, nil
	}

	err := runFetchBasemapWith(context.Background(), []string{"--out", t.TempDir()}, spec, runner)
	if err == nil || !strings.Contains(err.Error(), "generating toolchain is required") {
		t.Fatalf("runFetchBasemapWith error = %v, want required generating toolchain", err)
	}
	if called {
		t.Fatal("runner called with missing generating toolchain")
	}
}

func TestRunFetchBasemapReportsFailuresAndCleansTemporaryOutput(t *testing.T) {
	t.Parallel()
	spec, body := fetchTestBasemapSpec()
	wrongDigest := append([]byte(nil), body...)
	wrongDigest[0] ^= 0xff
	tests := []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		runner  basemapCommandRunner
		wantErr string
		isError error
	}{
		{
			name: "runner stderr",
			runner: func(context.Context, basemapCommand) ([]byte, error) {
				return []byte("upstream rejected range"), errors.New("exit status 1")
			},
			wantErr: "upstream rejected range",
		},
		{
			name: "missing output",
			runner: func(context.Context, basemapCommand) ([]byte, error) {
				return nil, nil
			},
			wantErr: "no such file",
		},
		{
			name: "wrong size",
			runner: func(_ context.Context, request basemapCommand) ([]byte, error) {
				return nil, os.WriteFile(request.Arguments[4], []byte("short"), 0o644)
			},
			wantErr: "size 5, want 12",
		},
		{
			name: "wrong digest",
			runner: func(_ context.Context, request basemapCommand) ([]byte, error) {
				return nil, os.WriteFile(request.Arguments[4], wrongDigest, 0o644)
			},
			wantErr: "sha256",
		},
		{
			name: "timeout cancellation",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Millisecond)
			},
			runner: func(ctx context.Context, _ basemapCommand) ([]byte, error) {
				<-ctx.Done()
				return []byte("command killed"), ctx.Err()
			},
			wantErr: "command killed",
			isError: context.DeadlineExceeded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if tc.ctx != nil {
				cancel()
				ctx, cancel = tc.ctx()
			}
			defer cancel()
			outDir := filepath.Join(t.TempDir(), "basemap")
			err := runFetchBasemapWith(ctx, []string{"--out", outDir}, spec, tc.runner)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("runFetchBasemapWith error = %v, want %q", err, tc.wantErr)
			}
			if tc.isError != nil && !errors.Is(err, tc.isError) {
				t.Fatalf("runFetchBasemapWith error = %v, want errors.Is %v", err, tc.isError)
			}
			assertBasemapDirectory(t, outDir)
		})
	}
}

func TestRunFetchBasemapPreservesExistingArchiveOnFailedRefresh(t *testing.T) {
	t.Parallel()
	spec, body := fetchTestBasemapSpec()
	outDir := t.TempDir()
	finalPath := filepath.Join(outDir, spec.Filename)
	if err := os.WriteFile(finalPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), body...)
	bad[len(bad)-1] ^= 0xff
	runner := func(_ context.Context, request basemapCommand) ([]byte, error) {
		return nil, os.WriteFile(request.Arguments[4], bad, 0o644)
	}

	err := runFetchBasemapWith(context.Background(), []string{"--out", outDir}, spec, runner)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("runFetchBasemapWith error = %v, want sha256", err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("preserved body = %q, want %q", got, body)
	}
	assertBasemapDirectory(t, outDir, spec.Filename)
}

func TestRunFetchBasemapCleansTemporaryOutputWhenFinalizationFails(t *testing.T) {
	t.Parallel()
	spec, body := fetchTestBasemapSpec()
	outDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(outDir, spec.Filename), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, request basemapCommand) ([]byte, error) {
		return nil, os.WriteFile(request.Arguments[4], body, 0o644)
	}

	err := runFetchBasemapWith(context.Background(), []string{"--out", outDir}, spec, runner)
	if err == nil || !strings.Contains(err.Error(), "publish basemap") {
		t.Fatalf("runFetchBasemapWith error = %v, want finalization failure", err)
	}
	assertBasemapDirectory(t, outDir, spec.Filename)
}

func TestRunFetchBasemapReturnsAndJoinsCleanupFailures(t *testing.T) {
	t.Parallel()
	spec, body := fetchTestBasemapSpec()
	outDir := t.TempDir()
	runnerErr := errors.New("extract failed")
	cleanupErr := errors.New("cleanup failed")
	runner := func(context.Context, basemapCommand) ([]byte, error) {
		return nil, runnerErr
	}
	cleanup := func(path string) error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return cleanupErr
	}

	err := runFetchBasemapWithCleanup(context.Background(), []string{"--out", outDir}, spec, runner, cleanup)
	if !errors.Is(err, runnerErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("runFetchBasemapWithCleanup error = %v, want joined runner and cleanup failures", err)
	}
	assertBasemapDirectory(t, outDir)

	successOut := t.TempDir()
	successRunner := func(_ context.Context, request basemapCommand) ([]byte, error) {
		return nil, os.WriteFile(request.Arguments[4], body, 0o644)
	}
	err = runFetchBasemapWithCleanup(context.Background(), []string{"--out", successOut}, spec, successRunner, cleanup)
	if !errors.Is(err, cleanupErr) || errors.Is(err, runnerErr) {
		t.Fatalf("successful extract cleanup error = %v, want cleanup failure only", err)
	}
	assertBasemapDirectory(t, successOut, spec.Filename)
}

func TestRunBasemapCommandKillsDescendantsOnCancellation(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "grandchild.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := runBasemapCommand(ctx, basemapCommand{
		Executable: os.Args[0],
		Arguments: []string{
			"-test.run=^TestBasemapCommandHelperProcess$", "--", "basemap-helper", pidPath,
		},
	})
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("runBasemapCommand error = %v, context error = %v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > basemapCommandWaitTime+2*time.Second {
		t.Fatalf("runBasemapCommand returned after %s, want at most %s", elapsed, basemapCommandWaitTime+2*time.Second)
	}

	body, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read grandchild PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatalf("parse grandchild PID %q: %v", body, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild process %d survived cancellation: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunBasemapCommandAppliesEnvironmentOverrides(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "go1.27.0")
	t.Setenv("WK_BASEMAP_INHERITED", "preserved")
	outPath := filepath.Join(t.TempDir(), "environment.txt")

	_, err := runBasemapCommand(context.Background(), basemapCommand{
		Executable: os.Args[0],
		Arguments: []string{
			"-test.run=^TestBasemapCommandEnvironmentHelperProcess$", "--", "basemap-environment-helper", outPath,
		},
		EnvOverrides: []string{"GOTOOLCHAIN=go1.26.7"},
	})
	if err != nil {
		t.Fatalf("runBasemapCommand: %v", err)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), "go1.26.7\npreserved\n"; got != want {
		t.Fatalf("child environment = %q, want %q", got, want)
	}
}

func TestBasemapCommandEnvironmentHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "basemap-environment-helper" {
		return
	}
	body := []byte(os.Getenv("GOTOOLCHAIN") + "\n" + os.Getenv("WK_BASEMAP_INHERITED") + "\n")
	if err := os.WriteFile(os.Args[len(os.Args)-1], body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBasemapCommandHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "basemap-helper" {
		return
	}
	pidPath := os.Args[len(os.Args)-1]
	child := exec.Command(os.Args[0], "-test.run=^TestBasemapCommandGrandchildProcess$", "--", "basemap-grandchild")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestBasemapCommandGrandchildProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "basemap-grandchild" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestGeoFingerprintIncludesCompleteBasemapRestoreContract(t *testing.T) {
	t.Parallel()
	base, _ := fetchTestBasemapSpec()
	want := geoFingerprint(base)
	tests := []struct {
		name   string
		mutate func(*ingest.BasemapSpec)
	}{
		{name: "source", mutate: func(s *ingest.BasemapSpec) { s.Source += "?changed=1" }},
		{name: "tool", mutate: func(s *ingest.BasemapSpec) { s.Tool += "-changed" }},
		{name: "Go toolchain", mutate: func(s *ingest.BasemapSpec) { s.GoToolchain += "-changed" }},
		{name: "bbox", mutate: func(s *ingest.BasemapSpec) { s.BBox += ",changed" }},
		{name: "maximum zoom", mutate: func(s *ingest.BasemapSpec) { s.MaxZoom++ }},
		{name: "overfetch", mutate: func(s *ingest.BasemapSpec) { s.Overfetch++ }},
		{name: "filename", mutate: func(s *ingest.BasemapSpec) { s.Filename = "changed.pmtiles" }},
		{name: "size", mutate: func(s *ingest.BasemapSpec) { s.Size++ }},
		{name: "digest", mutate: func(s *ingest.BasemapSpec) { s.SHA256 = strings.Repeat("0", 64) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			if got := geoFingerprint(changed); got == want {
				t.Fatalf("fingerprint unchanged after %s mutation", tc.name)
			}
		})
	}
	changed := base
	changed.Attribution = "display-only change"
	if got := geoFingerprint(changed); got != want {
		t.Fatalf("attribution changed fetch fingerprint: got %s, want %s", got, want)
	}
}

func fetchTestBasemapSpec() (ingest.BasemapSpec, []byte) {
	body := []byte("tiny-pmtiles")
	digest := sha256.Sum256(body)
	return ingest.BasemapSpec{
		Source: "https://example.test/source.pmtiles", Tool: "example.test/pmtiles@v1.2.3",
		GoToolchain: "go1.26.7",
		BBox:        "-1,-2,3,4", MaxZoom: 2, Overfetch: 0, Filename: "tiny.pmtiles",
		Size: int64(len(body)), SHA256: fmt.Sprintf("%x", digest), Attribution: "test attribution",
	}, body
}

func assertBasemapDirectory(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("output directory has %d entries, want %d: %v", len(entries), len(names), entries)
	}
	for i, entry := range entries {
		if entry.Name() != names[i] {
			t.Fatalf("output directory entry %d = %q, want %q", i, entry.Name(), names[i])
		}
	}
}
