package layout_test

import (
	"fmt"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestSelectLayouts(t *testing.T) {
	tests := []struct {
		name    string
		units   int
		opts    layout.Options
		x, y, z int
	}{
		{name: "defaults", units: 1, x: 1, y: 1, z: 1},
		{name: "balanced cube", units: 9, x: 3, y: 3, z: 3},
		{name: "balanced capped", units: 9, opts: layout.Options{MaxHeight: 16}, x: 3, y: 1, z: 3},
		{name: "compact prime", units: 5, opts: layout.Options{Policy: "compact"}, x: 1, y: 5, z: 1},
		{name: "compact height tie", units: 6, opts: layout.Options{Policy: "compact"}, x: 1, y: 3, z: 2},
		{name: "bounded zero", units: 5, opts: layout.Options{Policy: "bounded"}, x: 1, y: 5, z: 1},
		{name: "bounded floor", units: 3, opts: layout.Options{Policy: "bounded", PaddingPercent: 25}, x: 1, y: 3, z: 1},
		{name: "bounded allowance", units: 5, opts: layout.Options{Policy: "bounded", PaddingPercent: 25}, x: 1, y: 3, z: 2},
		{name: "bounded cube", units: 5, opts: layout.Options{Policy: "bounded", PaddingPercent: 100}, x: 2, y: 2, z: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := layout.Select(tt.units, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.X != tt.x*16 || got.Y != tt.y*16 || got.Z != tt.z*16 {
				t.Fatalf("dimensions = (%d,%d,%d), want (%d,%d,%d)", got.X, got.Y, got.Z, tt.x*16, tt.y*16, tt.z*16)
			}
			if got.Colors != nil {
				t.Fatal("Select allocated Colors")
			}
		})
	}
}

func TestSelectMatchesExhaustiveRectangles(t *testing.T) {
	for _, policy := range []string{"compact", "bounded"} {
		for _, padding := range []int{0, 1, 25, 50, 100} {
			for _, height := range []int{1, 2, 4, 24} {
				t.Run(fmt.Sprintf("%s/padding%d/height%d", policy, padding, height), func(t *testing.T) {
					for required := 1; required <= 64; required++ {
						opts := layout.Options{MaxHeight: height * 16, Policy: policy, PaddingPercent: padding}
						got, err := layout.Select(required, opts)
						if err != nil {
							t.Fatal(err)
						}
						want := exhaustive(required, height, padding, policy)
						if got.X/16 != want.x || got.Y/16 != want.y || got.Z/16 != want.z {
							t.Fatalf("required %d: got (%d,%d,%d), want (%d,%d,%d)",
								required, got.X/16, got.Y/16, got.Z/16, want.x, want.y, want.z)
						}
					}
				})
			}
		}
	}
}

type rectangle struct {
	x, y, z int
}

// This oracle visits every rectangle, including every z, without the
// production selector's pruning. Scores encode the README's ordered rules.
func exhaustive(required, height, padding int, policy string) rectangle {
	limit := required
	if policy == "bounded" {
		limit += required * padding / 100
	}
	var best rectangle
	bestScore := []int{}
	for y := 1; y <= height; y++ {
		for x := 1; x <= limit/y; x++ {
			for z := x; z <= limit/(x*y); z++ {
				total := x * y * z
				if total < required || total > limit {
					continue
				}
				spread := max(x, y, z) - min(x, y, z)
				squared := (x-y)*(x-y) + (y-z)*(y-z) + (z-x)*(z-x)
				score := []int{spread, squared, total, -y, x, z}
				if policy == "compact" {
					score = []int{total, spread, squared, -y, x, z}
				}
				if len(bestScore) == 0 || less(score, bestScore) {
					best = rectangle{x: x, y: y, z: z}
					bestScore = score
				}
			}
		}
	}
	return best
}

func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func TestSelectResourceLimits(t *testing.T) {
	tests := []struct {
		name  string
		units int
		opts  layout.Options
	}{
		{name: "zero", units: 0},
		{name: "negative", units: -1},
		{name: "oversized", units: volume.MaxBlocks/4096 + 1},
		{name: "integer overflow input", units: int(^uint(0) >> 1)},
		{name: "height too small", units: 1, opts: layout.Options{MaxHeight: 1}},
		{name: "height negative", units: 1, opts: layout.Options{MaxHeight: -16}},
		{name: "height fractional unit", units: 1, opts: layout.Options{MaxHeight: 17}},
		{name: "height too large", units: 1, opts: layout.Options{MaxHeight: 400}},
		{name: "padding negative", units: 1, opts: layout.Options{PaddingPercent: -1}},
		{name: "padding too large", units: 1, opts: layout.Options{PaddingPercent: 101}},
		{name: "unknown policy", units: 1, opts: layout.Options{Policy: "Balanced"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := layout.Select(tt.units, tt.opts); err == nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
	for _, policy := range []string{"balanced", "compact", "bounded"} {
		t.Run("maximum/"+policy, func(t *testing.T) {
			got, err := layout.Select(volume.MaxBlocks/4096, layout.Options{Policy: policy, PaddingPercent: 100})
			if err != nil {
				t.Fatal(err)
			}
			n, err := volume.Count(got.X, got.Y, got.Z)
			if err != nil || n != volume.MaxBlocks || got.Colors != nil || got.Y > layout.DefaultMaxHeight {
				t.Fatalf("maximum layout = %+v, count %d, error %v", got, n, err)
			}
		})
	}
}

func TestSelectHeightCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		units   int
		opts    layout.Options
		x, y, z int
	}{
		{name: "raised default cube", units: 4097, x: 17, y: 17, z: 17},
		{name: "default cube", units: 15625, x: 26, y: 24, z: 26},
		{name: "unlimited cube", units: 15625, opts: layout.Options{NoHeightLimit: true}, x: 25, y: 25, z: 25},
		{name: "default cap fallback", units: volume.MaxBlocks / 4096, x: 16, y: 16, z: 64},
		{name: "unlimited fallback", units: volume.MaxBlocks / 4096, opts: layout.Options{NoHeightLimit: true}, x: 16, y: 32, z: 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := layout.Select(tt.units, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.X != tt.x*16 || got.Y != tt.y*16 || got.Z != tt.z*16 {
				t.Fatalf("dimensions = (%d,%d,%d), want (%d,%d,%d)", got.X, got.Y, got.Z, tt.x*16, tt.y*16, tt.z*16)
			}
			n, err := volume.Count(got.X, got.Y, got.Z)
			if err != nil || n < tt.units*4096 {
				t.Fatalf("insufficient capacity: count %d, error %v", n, err)
			}
		})
	}
}
