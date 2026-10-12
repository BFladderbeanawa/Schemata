package volume

import "testing"

func TestCountDenseAndConcreteDimensions(t *testing.T) {
	cases := []struct {
		name    string
		x, y, z int
		valid   bool
	}{
		{name: "single position", x: 1, y: 1, z: 1, valid: true},
		{name: "arbitrary dimensions", x: 3, y: 5, z: 7, valid: true},
		{name: "maximum positions", x: MaxBlocks, y: 1, z: 1, valid: true},
		{name: "over product", x: MaxBlocks, y: 2, z: 1},
		{name: "zero", x: 0, y: 1, z: 1},
		{name: "negative", x: -1, y: 1, z: 1},
		{name: "oversize dimension", x: MaxBlocks + 1, y: 1, z: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, err := CountDense(tc.x, tc.y, tc.z)
			if (err == nil) != tc.valid {
				t.Fatalf("CountDense returned %d, %v", count, err)
			}
			if tc.valid && count != tc.x*tc.y*tc.z {
				t.Fatal("incorrect position count")
			}
		})
	}
	if _, err := Count(1, 1, 1); err == nil {
		t.Fatal("concrete accepted arbitrary dimensions")
	}
}

func TestValidateDenseReferencesAndBanners(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Volume)
	}{
		{name: "mixed storage", mutate: func(v *Volume) { v.Colors = []byte{0} }},
		{name: "missing position", mutate: func(v *Volume) { v.Blocks = v.Blocks[:1] }},
		{name: "palette bounds", mutate: func(v *Volume) { v.Blocks[0] = 2 }},
		{name: "missing banner", mutate: func(v *Volume) { delete(v.Banners, 1) }},
		{name: "nonbanner data", mutate: func(v *Volume) { v.Banners[0] = []Layer{} }},
		{name: "outside banner", mutate: func(v *Volume) { v.Banners[2] = []Layer{} }},
		{name: "negative banner", mutate: func(v *Volume) { v.Banners[-1] = []Layer{} }},
		{name: "seven layers", mutate: func(v *Volume) { v.Banners[1] = make([]Layer, 7) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Volume{
				X: 2, Y: 1, Z: 1, Blocks: []uint32{0, 1},
				Palette: []State{{Name: "minecraft:air"}, {Name: "minecraft:red_banner"}},
				Banners: map[int][]Layer{1: {}},
			}
			if err := v.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&v)
			if err := v.Validate(); err == nil {
				t.Fatal("accepted invalid dense storage")
			}
		})
	}
}
