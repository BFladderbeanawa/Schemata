package layout_test

import (
	"fmt"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestSelectDenseAvoidsDegenerateRectangles(t *testing.T) {
	t.Parallel()
	for _, required := range []int{67, 903335} {
		t.Run(fmt.Sprintf("positions%d", required), func(t *testing.T) {
			got, err := layout.SelectDense(required, layout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got.X != got.Z || got.Y != min(got.X, layout.DefaultMaxHeight) {
				t.Fatalf("degenerate rectangle: (%d,%d,%d)", got.X, got.Y, got.Z)
			}
			if got.X*got.Y*got.Z < required {
				t.Fatal("rectangle cannot hold stream")
			}
			previous := got.X - 1
			if previous*min(previous, layout.DefaultMaxHeight)*previous >= required {
				t.Fatal("rectangle wastes a complete outer layer")
			}
		})
	}
}

func TestSelectDenseLayouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		positions int
		opts      layout.Options
		x, y, z   int
	}{
		{name: "single position", positions: 1, x: 1, y: 1, z: 1},
		{name: "prime within height", positions: 61, opts: layout.Options{Policy: "compact"}, x: 1, y: 61, z: 1},
		{name: "prime within default cap", positions: 67, opts: layout.Options{Policy: "compact"}, x: 1, y: 67, z: 1},
		{
			name: "prime explicit cap", positions: 17,
			opts: layout.Options{Policy: "compact", MaxHeight: 16}, x: 1, y: 1, z: 17,
		},
		{
			name: "prime larger cap", positions: 67,
			opts: layout.Options{Policy: "compact", MaxHeight: 80}, x: 1, y: 67, z: 1,
		},
		{name: "compact height tie", positions: 6, opts: layout.Options{Policy: "compact"}, x: 1, y: 3, z: 2},
		{name: "compact horizontal tie", positions: 72, opts: layout.Options{Policy: "compact"}, x: 3, y: 6, z: 4},
		{name: "compact cube", positions: 64, opts: layout.Options{Policy: "compact"}, x: 4, y: 4, z: 4},
		{name: "balanced cube", positions: 9, opts: layout.Options{Policy: "balanced"}, x: 3, y: 3, z: 3},
		{
			name: "balanced capped", positions: 4097,
			opts: layout.Options{Policy: "balanced", MaxHeight: 16}, x: 17, y: 16, z: 17,
		},
		{name: "bounded zero", positions: 5, opts: layout.Options{Policy: "bounded"}, x: 1, y: 5, z: 1},
		{
			name: "bounded floor", positions: 3,
			opts: layout.Options{Policy: "bounded", PaddingPercent: 25}, x: 1, y: 3, z: 1,
		},
		{
			name: "bounded allowance", positions: 5,
			opts: layout.Options{Policy: "bounded", PaddingPercent: 25}, x: 1, y: 3, z: 2,
		},
		{
			name: "bounded cube", positions: 5,
			opts: layout.Options{Policy: "bounded", PaddingPercent: 100}, x: 2, y: 2, z: 2,
		},
		{
			name: "compact ignores padding", positions: 5,
			opts: layout.Options{Policy: "compact", PaddingPercent: 100}, x: 1, y: 5, z: 1,
		},
		{
			name: "balanced ignores padding", positions: 5,
			opts: layout.Options{Policy: "balanced", PaddingPercent: 0}, x: 2, y: 2, z: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := layout.SelectDense(tt.positions, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.X != tt.x || got.Y != tt.y || got.Z != tt.z {
				t.Fatalf("dimensions = (%d,%d,%d), want (%d,%d,%d)", got.X, got.Y, got.Z, tt.x, tt.y, tt.z)
			}
			if got.Colors != nil || got.Blocks != nil || got.Palette != nil || got.Banners != nil {
				t.Fatal("SelectDense allocated position data")
			}
			if count, err := volume.CountDense(got.X, got.Y, got.Z); err != nil || count < tt.positions {
				t.Fatalf("insufficient or invalid capacity: count %d, error %v", count, err)
			}
		})
	}
}

func TestSelectDenseMatchesExhaustiveRectangles(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"compact", "bounded"} {
		for _, padding := range []int{0, 1, 25, 50, 100} {
			for _, height := range []int{16, 32, 64, 384} {
				t.Run(fmt.Sprintf("%s/padding%d/height%d", policy, padding, height), func(t *testing.T) {
					t.Parallel()
					for required := 1; required <= 32; required++ {
						opts := layout.Options{MaxHeight: height, Policy: policy, PaddingPercent: padding}
						got, err := layout.SelectDense(required, opts)
						if err != nil {
							t.Fatal(err)
						}
						want := exhaustive(required, height, padding, policy)
						if got.X != want.x || got.Y != want.y || got.Z != want.z {
							t.Fatalf("required %d: got (%d,%d,%d), want (%d,%d,%d)",
								required, got.X, got.Y, got.Z, want.x, want.y, want.z)
						}
					}
				})
			}
		}
	}
}

func TestSelectDenseResourceLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		positions int
		opts      layout.Options
	}{
		{name: "zero", positions: 0},
		{name: "negative", positions: -1},
		{name: "oversized", positions: volume.MaxBlocks + 1},
		{name: "integer overflow input", positions: int(^uint(0) >> 1)},
		{name: "height too small", positions: 1, opts: layout.Options{MaxHeight: 1}},
		{name: "height negative", positions: 1, opts: layout.Options{MaxHeight: -16}},
		{name: "height fractional unit", positions: 1, opts: layout.Options{MaxHeight: 17}},
		{name: "height too large", positions: 1, opts: layout.Options{MaxHeight: 400}},
		{name: "padding negative", positions: 1, opts: layout.Options{PaddingPercent: -1}},
		{name: "padding too large", positions: 1, opts: layout.Options{PaddingPercent: 101}},
		{name: "unknown policy", positions: 1, opts: layout.Options{Policy: "Balanced"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := layout.SelectDense(tt.positions, tt.opts); err == nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
	for _, policy := range []string{"balanced", "compact", "bounded"} {
		t.Run("maximum/"+policy, func(t *testing.T) {
			t.Parallel()
			got, err := layout.SelectDense(volume.MaxBlocks, layout.Options{Policy: policy, PaddingPercent: 100})
			if err != nil {
				t.Fatal(err)
			}
			n, err := volume.CountDense(got.X, got.Y, got.Z)
			if err != nil || n != volume.MaxBlocks || got.Y > layout.DefaultMaxHeight {
				t.Fatalf("maximum layout = %+v, count %d, error %v", got, n, err)
			}
		})
	}
	for _, policy := range []string{"compact", "bounded"} {
		t.Run("maximum height/"+policy, func(t *testing.T) {
			t.Parallel()
			got, err := layout.SelectDense(volume.MaxBlocks, layout.Options{
				Policy: policy, MaxHeight: 384, PaddingPercent: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.X != 512 || got.Y != 256 || got.Z != 512 {
				t.Fatalf("maximum-height dimensions = (%d,%d,%d), want (512,256,512)", got.X, got.Y, got.Z)
			}
		})
	}
}

func TestSelectDenseHeightCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		positions int
		opts      layout.Options
		x, y, z   int
	}{
		{name: "raised default cube", positions: 4097, x: 17, y: 17, z: 17},
		{name: "default capped cube", positions: 56623105, x: 385, y: 384, z: 385},
		{name: "unlimited cube", positions: 56623105, opts: layout.Options{NoHeightLimit: true}, x: 385, y: 385, z: 385},
		{name: "default fallback", positions: volume.MaxBlocks, x: 256, y: 256, z: 1024},
		{name: "unlimited fallback", positions: volume.MaxBlocks, opts: layout.Options{NoHeightLimit: true}, x: 256, y: 512, z: 512},
		{name: "unlimited compact", positions: 67, opts: layout.Options{Policy: "compact", NoHeightLimit: true}, x: 1, y: 67, z: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := layout.SelectDense(tt.positions, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.X != tt.x || got.Y != tt.y || got.Z != tt.z {
				t.Fatalf("dimensions = (%d,%d,%d), want (%d,%d,%d)", got.X, got.Y, got.Z, tt.x, tt.y, tt.z)
			}
			if count, err := volume.CountDense(got.X, got.Y, got.Z); err != nil || count < tt.positions {
				t.Fatalf("insufficient capacity: count %d, error %v", count, err)
			}
			if !tt.opts.NoHeightLimit && got.Y > layout.DefaultMaxHeight {
				t.Fatalf("height %d exceeds the default cap", got.Y)
			}
		})
	}
}

func TestSelectDensePaddingAndDeterminism(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"compact", "bounded"} {
		for _, padding := range []int{0, 25, 100} {
			t.Run(fmt.Sprintf("%s/padding%d", policy, padding), func(t *testing.T) {
				t.Parallel()
				for _, required := range []int{1, 67, 1009, 4097, volume.MaxBlocks - 1} {
					opts := layout.Options{Policy: policy, MaxHeight: 16, PaddingPercent: padding}
					first, err := layout.SelectDense(required, opts)
					if err != nil {
						t.Fatal(err)
					}
					limit := int64(required)
					if policy == "bounded" {
						limit = min(int64(volume.MaxBlocks), limit+limit*int64(padding)/100)
					}
					count, err := volume.CountDense(first.X, first.Y, first.Z)
					outsideAllowance := count < required || int64(count) > limit
					if err != nil || outsideAllowance || first.Y > opts.MaxHeight {
						t.Fatalf("required %d: layout %+v, count %d, limit %d, error %v", required, first, count, limit, err)
					}
					for range 3 {
						again, err := layout.SelectDense(required, opts)
						if err != nil || again.X != first.X || again.Y != first.Y || again.Z != first.Z {
							t.Fatalf("nondeterministic selection: first %+v, next %+v, error %v", first, again, err)
						}
					}
				}
			})
		}
	}
}
