package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"wk/internal/blob"
	"wk/internal/ingest"
)

// warmEventsKey is where the normalized Wikidata event set lives in wk-warm;
// bake --warm and census read it from there (DEV-5).
const warmEventsKey = "wikidata/events.ndjson"

// runFetchWikidata pulls the bounded Wikidata event slice (M5): raw SPARQL
// pages go to wk-dumps for provenance (SRC-2), the normalized NDJSON to
// wk-warm for bake/census.
func runFetchWikidata(ctx context.Context) error {
	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 120 * time.Second}
	records, raw, stats, err := ingest.FetchWikidata(ctx, httpClient, func(msg string) {
		fmt.Println("  " + msg)
	})
	if err != nil {
		return err
	}
	fmt.Printf("fetched %d pages, %d rows, %d distinct entities\n", stats.Pages, stats.Rows, stats.Distinct)

	runID := time.Now().UTC().Format("20060102-150405")
	dumps := envOr("BUCKET_DUMPS", "wk-dumps")
	for i, page := range raw {
		key := fmt.Sprintf("wikidata/%s/page-%03d.json", runID, i)
		if _, err := cli.PutIfChanged(ctx, dumps, key, page, "application/json"); err != nil {
			return err
		}
	}

	body, err := ingest.EncodeWarmEvents(records)
	if err != nil {
		return err
	}
	warm := envOr("BUCKET_WARM", "wk-warm")
	if _, err := cli.PutIfChanged(ctx, warm, warmEventsKey, body, "application/x-ndjson"); err != nil {
		return err
	}
	fmt.Printf("raw pages -> s3://%s/wikidata/%s/  normalized -> s3://%s/%s\n", dumps, runID, warm, warmEventsKey)
	return nil
}
