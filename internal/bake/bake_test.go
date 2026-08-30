package bake

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"wk/internal/model"
	"wk/internal/rankzoom"
)

// memSink collects artifacts in memory and reports change like the S3 sink.
// Put is called from the writer's upload pool, so it must be goroutine-safe.
type memSink struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	keys    []string
}

func newMemSink() *memSink {
	return &memSink{objects: map[string][]byte{}, types: map[string]string{}}
}

func (m *memSink) Put(_ context.Context, key string, body []byte, contentType string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, key)
	m.types[key] = contentType
	if old, ok := m.objects[key]; ok && string(old) == string(body) {
		return false, nil
	}
	m.objects[key] = body
	return true, nil
}

type recordingCompiler struct {
	requests []LayerCompileRequest
	err      error
}

func (c *recordingCompiler) Compile(_ context.Context, request LayerCompileRequest) ([]byte, error) {
	c.requests = append(c.requests, request)
	if c.err != nil {
		return nil, c.err
	}
	return append([]byte("pmtiles:"), request.GeoJSON...), nil
}

type orderedSink struct {
	mu            sync.Mutex
	keys          []string
	failPMTiles   bool
	indexCtxError error
}

type blockingSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSink) Put(_ context.Context, key string, _ []byte, _ string) (bool, error) {
	if key == "v/test/entity/world-war-ii.json" {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return true, nil
}

func (s *orderedSink) Put(ctx context.Context, key string, _ []byte, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	if strings.HasSuffix(key, ".pmtiles") && s.failPMTiles {
		return false, fmt.Errorf("PMTiles upload failed")
	}
	if strings.HasSuffix(key, "/index.json") {
		s.indexCtxError = ctx.Err()
	}
	return true, nil
}

func testEntities(t *testing.T) []*model.Entity {
	t.Helper()
	es := []*model.Entity{
		{SeedID: "big-bang", Type: "natural_event", Name: "Big Bang",
			T0: model.YearToSeconds(-13.8e9), T1: model.YearToSeconds(-13.8e9),
			Precision: "billion_year", Status: "estimated",
			Categories: []string{"universe"}, Importance: 1.0},
		{SeedID: "ww2", Type: "event", Name: "World War II",
			T0: model.YearToSeconds(1939.67), T1: model.YearToSeconds(1945.67),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 1.0},
		{SeedID: "stalingrad", Type: "event", Name: "Battle of Stalingrad",
			T0: model.YearToSeconds(1942.64), T1: model.YearToSeconds(1943.09),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 0.9,
			Rel: []model.SeedRel{{Type: "part_of", Target: "ww2"}}},
		{SeedID: "obscure-skirmish", Type: "event", Name: "Obscure Skirmish",
			T0: model.YearToSeconds(1942.7), T1: model.YearToSeconds(1942.7),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 0.06},
	}
	if err := model.AssignSlugs(es); err != nil {
		t.Fatal(err)
	}
	if err := rankzoom.Bucketize(es); err != nil {
		t.Fatal(err)
	}
	return es
}

func TestBucketizeSemanticZoom(t *testing.T) {
	es := testEntities(t)
	byID := map[string]*model.Entity{}
	for _, e := range es {
		byID[e.SeedID] = e
	}
	if bb := byID["big-bang"]; bb.BucketMin != 0 || bb.BucketMax != 2 {
		t.Errorf("big bang buckets [%d,%d], want [0,2] (billion_year precision caps at T2)", bb.BucketMin, bb.BucketMax)
	}
	if ww := byID["ww2"]; ww.BucketMin != 0 || ww.BucketMax != 11 {
		// day precision would allow T13, but the 6-year span exceeds the
		// 1024-window cap at both T13 (~52k hours) and T12 (~2.2k days);
		// T11 (72 months) is the finest bucket under the cap.
		t.Errorf("ww2 buckets [%d,%d], want [0,11]", ww.BucketMin, ww.BucketMax)
	}
	if sk := byID["obscure-skirmish"]; sk.BucketMin < 12 {
		t.Errorf("importance 0.06 must not render above T12, got min %d", sk.BucketMin)
	}
}

func TestBakeChunksAndDocs(t *testing.T) {
	es := testEntities(t)
	sink := newMemSink()
	compiler := new(recordingCompiler)
	m, stats, err := Run(context.Background(), sink, compiler, "test", "seed-x", es, testGeo(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written == 0 {
		t.Fatal("nothing written")
	}

	// T0 world chunk holds only the high-importance entities.
	var t0 chunkFile
	mustGet(t, sink, "v/test/chunks/T0/0/world/all.json", &t0)
	names := []string{}
	for _, i := range t0.Items {
		names = append(names, i.Name)
	}
	if len(t0.Items) != 2 || !contains(names, "Big Bang") || !contains(names, "World War II") {
		t.Errorf("T0/all = %v, want Big Bang + WWII only", names)
	}

	// Stalingrad appears in the war chunk of its 1942 window at T10.
	w := model.Buckets[10].WindowIndex(model.YearToSeconds(1942.7))
	var t10 chunkFile
	mustGet(t, sink, keyFor("test", 10, w, "war"), &t10)
	names = names[:0]
	for _, i := range t10.Items {
		names = append(names, i.Name)
	}
	if !contains(names, "Battle of Stalingrad") {
		t.Errorf("T10 war window missing Stalingrad: %v", names)
	}

	// The parent's child_count reflects part_of.
	var ww2 chunkFile
	mustGet(t, sink, keyFor("test", 8, model.Buckets[8].WindowIndex(model.YearToSeconds(1940)), "all"), &ww2)
	for _, i := range ww2.Items {
		if i.Name == "World War II" && i.ChildCount != 1 {
			t.Errorf("ww2 child_count = %d, want 1", i.ChildCount)
		}
	}

	// Entity doc: slug key, part_of visible from both sides.
	var doc EntityDoc
	mustGet(t, sink, "v/test/entity/world-war-ii.json", &doc)
	if len(doc.Children) != 1 || doc.Children[0].Slug != "battle-of-stalingrad" {
		t.Errorf("ww2 children = %+v", doc.Children)
	}

	// Manifest window lists are per category and only contain baked windows.
	if ws := m.Buckets[0].Windows["all"]; len(ws) != 1 || ws[0] != 0 {
		t.Errorf("T0 all-windows = %v", m.Buckets[0].Windows)
	}
	if ws := m.Buckets[0].Windows["war"]; len(ws) != 1 || ws[0] != 0 {
		t.Errorf("T0 war-windows = %v", m.Buckets[0].Windows)
	}
	// A category with no entities in a window must not list that window:
	// the client would 404 on it (the browser-found filtering bug).
	w1942 := model.Buckets[10].WindowIndex(model.YearToSeconds(1942.7))
	if !slices.Contains(m.Buckets[10].Windows["war"], w1942) {
		t.Errorf("T10 war windows missing 1942: %v", m.Buckets[10].Windows["war"])
	}
	if slices.Contains(m.Buckets[10].Windows["universe"], w1942) {
		t.Errorf("T10 universe windows should not contain 1942: %v", m.Buckets[10].Windows["universe"])
	}

	// Idempotency: a second run writes nothing.
	_, stats2, err := Run(context.Background(), sink, compiler, "test", "seed-x", es, testGeo(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Written != 0 {
		t.Errorf("re-bake wrote %d artifacts, want 0", stats2.Written)
	}
}

func testGeo() *model.GeoSet {
	return &model.GeoSet{
		Borders: []model.BorderLayer{{
			Year: 1942, TFrom: 1939, TTo: 1945, Label: "Axis maximum", Source: "atlas",
			Features: []model.BorderFeature{{
				Name: "Axis", Entity: "ww2", Slug: "world-war-ii", Representation: "estimated",
				Geometry: json.RawMessage(`{"type":"Polygon","coordinates":[[[0,50],[20,50],[20,60],[0,50]]]}`),
			}},
		}},
		Fronts: map[string][]model.FrontPosition{
			"stalingrad": {
				{ValidFrom: model.YearToSeconds(1942.7), Label: "encirclement", Representation: "estimated",
					Source: "atlas", Coordinates: [][2]float64{{44, 48}, {45, 49}}},
				{ValidFrom: model.YearToSeconds(1943.0), Label: "surrender", Representation: "estimated",
					Source: "atlas", Coordinates: [][2]float64{{44.5, 48.5}, {45.5, 49.5}}},
			},
		},
	}
}

func TestBakeLayersAndGeometry(t *testing.T) {
	es := testEntities(t)
	sink := newMemSink()
	compiler := new(recordingCompiler)
	m, _, err := Run(context.Background(), sink, compiler, "test", "seed-x", es, testGeo(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(compiler.requests) != 1 {
		t.Fatalf("compiler requests = %d, want 1", len(compiler.requests))
	}
	request := compiler.requests[0]
	if request.Layer != BordersLayer || request.Year != 1942 || request.TFrom != 1939 || request.TTo != 1945 || request.Label != "Axis maximum" || request.Source != "atlas" {
		t.Fatalf("compiler request metadata = %#v", request)
	}
	if request.Attribution != BordersAttribution {
		t.Fatalf("compiler attribution = %q", request.Attribution)
	}
	var layer struct {
		Type       string `json:"type"`
		Properties struct {
			Year   int    `json:"year"`
			TFrom  int    `json:"t_from"`
			TTo    int    `json:"t_to"`
			Label  string `json:"label"`
			Source string `json:"source"`
		} `json:"properties"`
		Features []struct {
			Properties struct{ Slug, Representation, Color string } `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(request.GeoJSON, &layer); err != nil {
		t.Fatalf("compiler GeoJSON: %v", err)
	}
	if layer.Type != "FeatureCollection" {
		t.Errorf("layer type = %q", layer.Type)
	}
	if layer.Properties.TFrom != 1939 || layer.Properties.TTo != 1945 {
		t.Errorf("layer window = %d..%d, want 1939..1945", layer.Properties.TFrom, layer.Properties.TTo)
	}
	if len(layer.Features) != 1 || layer.Features[0].Properties.Slug != "world-war-ii" {
		t.Errorf("layer features = %+v, want the seed id resolved to a slug", layer.Features)
	}
	if layer.Features[0].Properties.Color != polityColor("Axis") {
		t.Errorf("layer color = %q", layer.Features[0].Properties.Color)
	}
	layerKey := "v/test/" + LayerKey(BordersLayer, 1942)
	if got := string(sink.objects[layerKey]); !strings.HasPrefix(got, "pmtiles:") {
		t.Fatalf("layer body = %q", got)
	}
	if sink.types[layerKey] != PMTilesContentType {
		t.Fatalf("layer MIME = %q", sink.types[layerKey])
	}

	// The index lets the client answer "is any era covering this date?" with
	// one small fetch instead of one snapshot per guess.
	var index layerIndex
	mustGet(t, sink, "v/test/"+LayerIndexKey(BordersLayer), &index)
	if len(index.Steps) != 1 || index.Steps[0].Year != 1942 || index.Steps[0].Source != "atlas" {
		t.Errorf("layer index = %+v", index.Steps)
	}
	if !strings.HasSuffix(layerKey, ".pmtiles") {
		t.Fatalf("layer key = %q", layerKey)
	}

	if m.Layers[0] != BordersLayer || len(m.Timesteps[BordersLayer]) != 1 {
		t.Errorf("manifest layers=%v timesteps=%v", m.Layers, m.Timesteps)
	}

	// Front positions ride on the owning entity's document (DM-7), and only
	// on that one.
	var doc EntityDoc
	mustGet(t, sink, "v/test/entity/battle-of-stalingrad.json", &doc)
	if len(doc.Geometry) != 2 {
		t.Fatalf("stalingrad geometry records = %d, want 2", len(doc.Geometry))
	}
	if doc.Geometry[0].Geometry.Type != "LineString" || len(doc.Geometry[0].Geometry.Coordinates) != 2 {
		t.Errorf("geometry record = %+v", doc.Geometry[0])
	}
	if doc.Geometry[0].Source == "" {
		t.Error("geometry record must carry its source")
	}
	var ww2 EntityDoc
	mustGet(t, sink, "v/test/entity/world-war-ii.json", &ww2)
	if ww2.Geometry != nil {
		t.Errorf("ww2 has no curated front, but got %d geometry records", len(ww2.Geometry))
	}
}

// Without curated geometry the manifest keeps the shape M2 published.
func TestBakeWithoutGeo(t *testing.T) {
	m, _, err := Run(context.Background(), newMemSink(), nil, "test", "seed-x", testEntities(t), &model.GeoSet{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 0 || len(m.Timesteps) != 0 {
		t.Errorf("layers=%v timesteps=%v, want both empty", m.Layers, m.Timesteps)
	}
}

func TestBakeAreaLayerRequiresCompiler(t *testing.T) {
	_, _, err := Run(context.Background(), newMemSink(), nil, "test", "seed-x", testEntities(t), testGeo(), nil)
	if err == nil || !strings.Contains(err.Error(), "layer compiler") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestBakeAreaLayerPropagatesCompilerFailure(t *testing.T) {
	compiler := &recordingCompiler{err: fmt.Errorf("compiler exploded")}
	_, _, err := Run(context.Background(), newMemSink(), compiler, "test", "seed-x", testEntities(t), testGeo(), nil)
	if err == nil || !strings.Contains(err.Error(), "compile borders layer 1942") || !strings.Contains(err.Error(), "compiler exploded") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRunDrainsOutstandingUploadsAfterCompilerFailure(t *testing.T) {
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	compiler := &recordingCompiler{err: fmt.Errorf("compiler exploded")}
	entities := testEntities(t)
	geo := testGeo()
	result := make(chan error, 1)
	go func() {
		_, _, err := Run(context.Background(), sink, compiler, "test", "seed-x", entities, geo, nil)
		result <- err
	}()

	<-sink.started
	select {
	case err := <-result:
		close(sink.release)
		t.Fatalf("Run returned before upload drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "compiler exploded") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestBakePaleoLayerUsesFixedAttributionAndOmitsColor(t *testing.T) {
	layer := model.BorderLayer{
		Year: -540_000_000, TFrom: -600_000_000, TTo: -500_000_000,
		Label: "Cambrian", Source: "MERDITH2021",
		Features: []model.BorderFeature{{
			Name: "land", Representation: "reconstructed",
			Geometry: json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[1,0],[0,1],[0,0]]]}`),
		}},
	}
	compiler := new(recordingCompiler)
	w := newWriter(context.Background(), newMemSink(), &Stats{})
	if _, err := bakeAreaLayer(context.Background(), w, compiler, "test", PaleoLayer, []model.BorderLayer{layer}); err != nil {
		t.Fatalf("bakeAreaLayer: %v", err)
	}
	if err := w.wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(compiler.requests) != 1 || compiler.requests[0].Attribution != PaleoAttribution {
		t.Fatalf("compiler requests = %#v", compiler.requests)
	}
	var doc struct {
		Features []struct {
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(compiler.requests[0].GeoJSON, &doc); err != nil {
		t.Fatalf("decode GeoJSON: %v", err)
	}
	if _, ok := doc.Features[0].Properties["color"]; ok {
		t.Fatalf("paleo properties contain color: %#v", doc.Features[0].Properties)
	}
}

func TestBakeAreaLayerDoesNotPublishIndexAfterBodyFailure(t *testing.T) {
	sink := &orderedSink{failPMTiles: true}
	w := newWriter(context.Background(), sink, &Stats{})
	_, err := bakeAreaLayer(context.Background(), w, new(recordingCompiler), "test", BordersLayer, testGeo().Borders)
	if err == nil || !strings.Contains(err.Error(), "PMTiles upload failed") {
		t.Fatalf("bakeAreaLayer error = %v", err)
	}
	for _, key := range sink.keys {
		if strings.HasSuffix(key, "/index.json") {
			t.Fatalf("index published after body failure: %v", sink.keys)
		}
	}
}

func TestBakeAreaLayerIndexUsesFreshLiveContextAfterFlush(t *testing.T) {
	sink := new(orderedSink)
	w := newWriter(context.Background(), sink, &Stats{})
	layers := append([]model.BorderLayer(nil), testGeo().Borders...)
	second := layers[0]
	second.Year = 1943
	layers = append(layers, second)
	if _, err := bakeAreaLayer(context.Background(), w, new(recordingCompiler), "test", BordersLayer, layers); err != nil {
		t.Fatalf("bakeAreaLayer: %v", err)
	}
	if err := w.wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if sink.indexCtxError != nil {
		t.Fatalf("index context error = %v", sink.indexCtxError)
	}
	if len(sink.keys) != 3 || !strings.HasSuffix(sink.keys[0], ".pmtiles") || !strings.HasSuffix(sink.keys[1], ".pmtiles") || !strings.HasSuffix(sink.keys[2], "/index.json") {
		t.Fatalf("publication order = %v", sink.keys)
	}
}

func TestPolityColorMatchesJavaScriptUTF16(t *testing.T) {
	tests := map[string]string{
		"Axis":     "hsl(84, 34%, 45%)",
		"Québec":   "hsl(232, 34%, 45%)",
		"𐍈 Empire": "hsl(283, 34%, 45%)",
	}
	for name, want := range tests {
		if got := polityColor(name); got != want {
			t.Errorf("polityColor(%q) = %q, want %q", name, got, want)
		}
	}
}

func keyFor(ds string, bucket int, window int64, cat string) string {
	return fmt.Sprintf("v/%s/chunks/%s/%d/world/%s.json", ds, model.Buckets[bucket].ID, window, cat)
}

func mustGet(t *testing.T, s *memSink, key string, v any) {
	t.Helper()
	body, ok := s.objects[key]
	if !ok {
		sample := []string{}
		for k := range s.objects {
			if len(sample) < 10 && strings.Contains(k, "chunks") {
				sample = append(sample, k)
			}
		}
		t.Fatalf("missing artifact %s (sample keys: %v)", key, sample)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
}

func contains(ss []string, want string) bool {
	return slices.Contains(ss, want)
}
