package model

import "testing"

func TestWindowIndex(t *testing.T) {
	t10 := Buckets[10] // year windows
	if t10.ID != "T10" || t10.WindowS != SecondsPerYear {
		t.Fatalf("bucket table order broken: got %s window %v", t10.ID, t10.WindowS)
	}
	cases := []struct {
		name string
		b    Bucket
		t    float64
		want int64
	}{
		{"epoch", t10, 0, 0},
		{"within first year", t10, SecondsPerYear - 1, 0},
		{"second year", t10, SecondsPerYear, 1},
		{"negative floors down", t10, -1, -1},
		{"one year before epoch", t10, -SecondsPerYear, -1},
		{"just past a year before epoch", t10, -SecondsPerYear - 1, -2},
		{"single-window bucket ignores t", Buckets[0], -4.35e17, 0},
		{"single-window far future", Buckets[0], 3e107, 0},
	}
	for _, c := range cases {
		if got := c.b.WindowIndex(c.t); got != c.want {
			t.Errorf("%s: WindowIndex(%v)=%d, want %d", c.name, c.t, got, c.want)
		}
	}
}

func TestBucketTableShape(t *testing.T) {
	if len(Buckets) != 14 {
		t.Fatalf("expected 14 buckets T0..T13, got %d", len(Buckets))
	}
	for i, b := range Buckets {
		if i <= 2 && b.WindowS != 0 {
			t.Errorf("%s must be single-window (far past/future overflow guard)", b.ID)
		}
		if i > 2 && b.WindowS <= 0 {
			t.Errorf("%s must have a positive window", b.ID)
		}
		if i > 3 && Buckets[i-1].WindowS > 0 && b.WindowS >= Buckets[i-1].WindowS {
			t.Errorf("%s window must be finer than %s", b.ID, Buckets[i-1].ID)
		}
	}
	// int64 safety: coarsest windowed bucket at the windowed-time bound.
	idx := Buckets[3].WindowIndex(MaxWindowedTime)
	if idx <= 0 {
		t.Errorf("T3 index at MaxWindowedTime should be positive, got %d", idx)
	}
}
