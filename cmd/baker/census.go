package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"time"

	"wk/internal/blob"
	"wk/internal/ingest"
	"wk/internal/model"
)

// runCensus quantifies the raw material (ROAD-2): per-era and per-type counts
// plus coverage (coordinates, precision) over seed + warm data. The report
// lands in wk-warm/reports/ and prints as a table.
func runCensus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("census", flag.ContinueOnError)
	seedDir := fs.String("seed", "data/seed", "NDJSON seed directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}
	res, err := ingest.LoadSeed(*seedDir)
	if err != nil {
		return err
	}
	warmBucket := envOr("BUCKET_WARM", "wk-warm")
	if warm, err := cli.Get(ctx, warmBucket, warmEventsKey); err == nil {
		added, skipped, err := ingest.MergeWarmEvents(res, warm)
		if err != nil {
			return err
		}
		fmt.Printf("warm events: %d merged, %d deduped against seed\n", added, skipped)
	} else {
		fmt.Println("no warm events found (run fetch-wikidata first); census covers the seed only")
	}

	type eraStat struct {
		Era      string         `json:"era"`
		Count    int            `json:"count"`
		ByType   map[string]int `json:"by_type"`
		HasPoint int            `json:"has_point"`
		DayPrec  int            `json:"day_precision"`
	}
	eras := []struct {
		name   string
		y0, y1 float64
	}{
		{"deep time (<10 kyr BCE)", -14e9, -10000},
		{"prehistory (10k-1000 BCE)", -10000, -1000},
		{"ancient (1000 BCE-500 CE)", -1000, 500},
		{"medieval (500-1500)", 500, 1500},
		{"early modern (1500-1800)", 1500, 1800},
		{"19th century", 1800, 1900},
		{"20th century", 1900, 2000},
		{"21st century", 2000, 2100},
		{"future (>2100)", 2100, 2e103},
	}
	stats := make([]eraStat, len(eras))
	for i, e := range eras {
		stats[i] = eraStat{Era: e.name, ByType: map[string]int{}}
	}
	for _, ent := range res.Entities {
		y := model.SecondsToYear(ent.T0)
		for i, e := range eras {
			if y >= e.y0 && y < e.y1 {
				s := &stats[i]
				s.Count++
				s.ByType[ent.Type]++
				if ent.Point != nil {
					s.HasPoint++
				}
				if ent.Precision == "day" || ent.Precision == "hour" || ent.Precision == "minute" {
					s.DayPrec++
				}
				break
			}
		}
	}

	fmt.Printf("\n%-28s %7s %9s %9s  top types\n", "ERA", "COUNT", "W/POINT", "DAY-PREC")
	for _, s := range stats {
		fmt.Printf("%-28s %7d %9d %9d  %s\n", s.Era, s.Count, s.HasPoint, s.DayPrec, topTypes(s.ByType, 3))
	}
	fmt.Printf("%-28s %7d\n", "TOTAL", len(res.Entities))

	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"seed_version": res.SeedVersion,
		"total":        len(res.Entities),
		"rejects":      len(res.Rejects),
		"eras":         stats,
	}
	key := fmt.Sprintf("reports/census-%s.json", time.Now().UTC().Format("20060102-150405"))
	if _, err := cli.PutJSON(ctx, warmBucket, key, report); err != nil {
		return err
	}
	fmt.Printf("\nreport -> s3://%s/%s\n", warmBucket, key)
	return nil
}

func topTypes(m map[string]int, n int) string {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	out := ""
	for i, e := range all {
		if i >= n {
			break
		}
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s:%d", e.k, e.v)
	}
	return out
}
