package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Writing side of the data/geo curated format that geo.go reads back. The
// fetch subcommands produce files a curator could have hand-written, so there
// is exactly one format and one validator for both origins.

type curatedSlice struct {
	Type       string            `json:"type"`
	Properties curatedSliceProps `json:"properties"`
	Features   []curatedFeature  `json:"features"`
}

type curatedSliceProps struct {
	Year   int    `json:"year"`
	TFrom  int    `json:"t_from"`
	TTo    int    `json:"t_to"`
	Label  string `json:"label"`
	Source string `json:"source"`
}

type curatedFeature struct {
	Type       string              `json:"type"`
	Properties curatedFeatureProps `json:"properties"`
	Geometry   json.RawMessage     `json:"geometry"`
}

type curatedFeatureProps struct {
	Name           string `json:"name"`
	Representation string `json:"representation"`
}

func encodeCuratedSlice(year, tFrom, tTo int, label, source string, feats []curatedFeature) ([]byte, error) {
	doc := curatedSlice{
		Type: "FeatureCollection",
		Properties: curatedSliceProps{
			Year: year, TFrom: tFrom, TTo: tTo, Label: label, Source: source,
		},
		Features: feats,
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// FormatYear renders a signed year the way a map chip should read it.
func FormatYear(y int) string {
	if y < 0 {
		return strconv.Itoa(-y) + " BC"
	}
	return strconv.Itoa(y)
}

func getJSON(ctx context.Context, cli *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("get %s: HTTP %d: %s", url, res.StatusCode, bytes.TrimSpace(snippet))
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("parse %s: %w", url, err)
	}
	return nil
}
