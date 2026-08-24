package model

// Manifest is the only mutable serving object (API-0). The client loads it once
// and computes every artifact key locally from it.
type Manifest struct {
	Dataset     string           `json:"dataset"`
	GeneratedAt string           `json:"generated_at"`
	SeedVersion string           `json:"seed_version,omitempty"`
	Buckets     []Bucket         `json:"buckets"`
	Categories  []string         `json:"categories"`
	Layers      []string         `json:"layers"`
	Timesteps   map[string][]int `json:"timesteps"`
	Counts      map[string]int   `json:"counts"`
	GoldenViews string           `json:"golden_views,omitempty"` // "pass" required to publish (ZOOM-5)
}
