package model

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Tyrannosaurus rex":      "tyrannosaurus-rex",
		"Battle of  Waterloo!":   "battle-of-waterloo",
		"Kärnten & Über-Straße":  "karnten-uber-strasse",
		"  --Weird--  edges--  ": "weird-edges",
		"La Tène culture":        "la-tene-culture",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAssignSlugsCollision(t *testing.T) {
	es := []*Entity{
		{SeedID: "mercury-planet", Name: "Mercury", Type: "astronomical_object", Importance: 0.9, T0: YearToSeconds(-4.5e9)},
		{SeedID: "mercury-element", Name: "Mercury", Type: "chemical", Importance: 0.5, T0: YearToSeconds(-1500)},
	}
	if err := AssignSlugs(es); err != nil {
		t.Fatal(err)
	}
	if es[0].Slug != "mercury" {
		t.Errorf("highest importance should keep bare slug, got %q", es[0].Slug)
	}
	// Year disambiguator first (DM-2a order): -1500 start year.
	if es[1].Slug != "mercury--1500" && es[1].Slug != "mercury-chemical" {
		t.Errorf("unexpected disambiguated slug %q", es[1].Slug)
	}
	if err := AssignSlugs([]*Entity{
		{SeedID: "a", Name: "Same"}, {SeedID: "a", Name: "Same"},
	}); err == nil {
		t.Error("duplicate seed ids must error")
	}
}

func TestAssignSlugsDeterministic(t *testing.T) {
	mk := func() []*Entity {
		return []*Entity{
			{SeedID: "b", Name: "Alpha", Importance: 0.5, T0: YearToSeconds(1900)},
			{SeedID: "a", Name: "Alpha", Importance: 0.5, T0: YearToSeconds(1800)},
		}
	}
	e1, e2 := mk(), mk()
	if err := AssignSlugs(e1); err != nil {
		t.Fatal(err)
	}
	if err := AssignSlugs(e2); err != nil {
		t.Fatal(err)
	}
	for i := range e1 {
		if e1[i].SeedID != e2[i].SeedID || e1[i].Slug != e2[i].Slug {
			t.Errorf("non-deterministic slugs: %v vs %v", e1[i], e2[i])
		}
	}
	// Equal importance: lexicographically smaller seed id wins the bare slug.
	for _, e := range e1 {
		if e.SeedID == "a" && e.Slug != "alpha" {
			t.Errorf("seed id 'a' should keep bare slug, got %q", e.Slug)
		}
	}
}
