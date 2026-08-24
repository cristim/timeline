package model

import (
	"encoding/json"
	"os"
	"testing"
)

// The committed fixture pins the Go and TypeScript window-index
// implementations to identical results (API-5: key-scheme drift between baker
// and client is the failure mode of this architecture). The web test suite
// asserts the same file.
func TestKeycasesFixture(t *testing.T) {
	b, err := os.ReadFile("../../web/src/lib/keycases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var cases []struct {
		Bucket string  `json:"bucket"`
		T      float64 `json:"t"`
		Window int64   `json:"window"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 100 {
		t.Fatalf("suspiciously small fixture: %d cases", len(cases))
	}
	byID := map[string]Bucket{}
	for _, bk := range Buckets {
		byID[bk.ID] = bk
	}
	for _, c := range cases {
		bk, ok := byID[c.Bucket]
		if !ok {
			t.Fatalf("fixture references unknown bucket %q", c.Bucket)
		}
		if got := bk.WindowIndex(c.T); got != c.Window {
			t.Errorf("%s WindowIndex(%v) = %d, fixture says %d", c.Bucket, c.T, got, c.Window)
		}
	}
}
