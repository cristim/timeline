package bake

import (
	"context"
	"strings"
	"testing"

	"wk/internal/model"
)

func goldensFor(views ...GoldenView) *GoldenFile {
	return &GoldenFile{SeedVersion: "seed-x", Views: views}
}

func TestGoldenGateBlocksPublish(t *testing.T) {
	es := testEntities(t)
	sink := newMemSink()

	// A view demanding a slug that is not in the chunk must fail the run
	// before any manifest exists.
	bad := goldensFor(GoldenView{
		Name: "impossible", Bucket: "T0", Category: "all",
		Include: []string{"entity-that-does-not-exist"},
	})
	m, _, err := Run(context.Background(), sink, nil, "test", "seed-x", es, &model.GeoSet{}, bad)
	if err == nil || m != nil {
		t.Fatalf("failing golden view must abort the bake, got m=%v err=%v", m, err)
	}
	if !strings.Contains(err.Error(), "entity-that-does-not-exist") {
		t.Errorf("error should name the missing slug: %v", err)
	}

	// The same bake with a satisfied view passes and stamps the manifest.
	good := goldensFor(GoldenView{
		Name: "t0", Bucket: "T0", Category: "all",
		Include: []string{"big-bang"}, Exclude: []string{"obscure-skirmish"},
	})
	m, _, err = Run(context.Background(), sink, nil, "test", "seed-x", es, &model.GeoSet{}, good)
	if err != nil {
		t.Fatal(err)
	}
	if m.GoldenViews != "pass" {
		t.Errorf("manifest golden_views = %q, want pass", m.GoldenViews)
	}
}

func TestGoldenSeedVersionPin(t *testing.T) {
	es := testEntities(t)
	stale := goldensFor(GoldenView{Name: "t0", Bucket: "T0", Category: "all"})
	stale.SeedVersion = "seed-older"
	_, _, err := Run(context.Background(), newMemSink(), nil, "test", "seed-x", es, &model.GeoSet{}, stale)
	if err == nil || !strings.Contains(err.Error(), "seed-older") {
		t.Fatalf("stale golden pin must fail with an explicit message, got %v", err)
	}
}

func TestGoldenWindowResolution(t *testing.T) {
	year := 1942.5
	v := GoldenView{Name: "x", Bucket: "T10", Year: &year, Category: "war"}
	key, err := v.ChunkKey()
	if err != nil {
		t.Fatal(err)
	}
	if key != "chunks/T10/-28/world/war.json" {
		t.Errorf("key = %q, want chunks/T10/-28/world/war.json", key)
	}
	v = GoldenView{Name: "x", Bucket: "T99", Category: "all"}
	if _, err := v.ChunkKey(); err == nil {
		t.Error("unknown bucket must error")
	}
}
