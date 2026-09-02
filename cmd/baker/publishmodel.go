package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"wk/internal/blob"
	"wk/internal/duck"
)

func runPublishWikidataModel(ctx context.Context, args []string) error {
	return runPublishWikidataModelWithIO(ctx, args, os.Stdout, os.Stderr)
}

func runPublishWikidataModelWithIO(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	fs := flag.NewFlagSet("publish-wikidata-model", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modelPath := fs.String("model", "", "committed model directory produced by ingest-wikidata-dump")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *modelPath == "" {
		return errors.New("--model <dir> is required")
	}
	committed, err := duck.OpenCommittedModel(ctx, *modelPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, committed.Close()) }()
	client, err := blob.New(ctx)
	if err != nil {
		return err
	}
	bucket := envOr("BUCKET_WARM", "wk-warm")
	manifest, err := publishOpenedCommittedWikidataDump(ctx, committed, client, bucket, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "report -> s3://%s/%s\n", bucket, manifest.Report.Key)
	fmt.Fprintf(stdout, "model  -> s3://%s/model/%s/\n", bucket, committed.ModelVersion())
	return nil
}
