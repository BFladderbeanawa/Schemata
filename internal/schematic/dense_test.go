package schematic

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func denseFixture(states []volume.State) volume.Volume {
	palette := make([]volume.State, 16, 16+len(states))
	for i, color := range colors {
		palette[i] = volume.State{Name: "minecraft:" + color + "_concrete", Properties: map[string]string{}}
	}
	palette = append(palette, states...)
	v := volume.Volume{
		X: 18 + len(states), Y: 1, Z: 1, Palette: palette,
		Blocks: make([]uint32, 18+len(states)), Banners: map[int][]volume.Layer{},
	}
	prefix := []byte("SCHEMATA\x02")
	for i, value := range prefix {
		v.Blocks[2*i], v.Blocks[2*i+1] = uint32(value>>4), uint32(value&15)
	}
	for i := range states {
		v.Blocks[18+i] = uint32(16 + i)
		if alphabet.IsBanner(states[i]) {
			v.Banners[18+i] = []volume.Layer{}
		}
	}
	return v
}

func assertDenseVolume(t *testing.T, got, want volume.Volume) {
	t.Helper()
	if got.Blocks == nil || got.X != want.X || got.Y != want.Y || got.Z != want.Z || len(got.Blocks) != len(want.Blocks) {
		t.Fatal("dense dimensions or representation changed")
	}
	for i, index := range want.Blocks {
		if !reflect.DeepEqual(got.Palette[got.Blocks[i]], want.Palette[index]) {
			t.Fatalf("state changed at native index %d", i)
		}
		if !reflect.DeepEqual([]volume.Layer(got.Banners[i]), []volume.Layer(want.Banners[i])) &&
			(len(got.Banners[i]) != 0 || len(want.Banners[i]) != 0) {
			t.Fatalf("ordered banner layers changed at native index %d", i)
		}
	}
}

// reverseCompoundNBT deliberately differs from the canonical writer at every
// compound level while leaving native lists, notably Patterns, ordered.
func reverseCompoundNBT(w *nbtWriter, value tag) {
	switch value.kind {
	case 10:
		keys := make([]string, 0, len(value.compound))
		for key := range value.compound {
			keys = append(keys, key)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		for _, key := range keys {
			child := value.compound[key]
			w.number(int64(child.kind), 1)
			w.string(key)
			reverseCompoundNBT(w, child)
		}
		w.number(0, 1)
	case 9:
		w.number(int64(value.element), 1)
		w.number(int64(len(value.list)), 4)
		for _, child := range value.list {
			reverseCompoundNBT(w, child)
		}
	default:
		w.payload(value)
	}
}

func TestDenseCompoundOrderingAndCompression(t *testing.T) {
	want := denseBannerFixture(t)
	root, err := denseLitematicRoot(want)
	if err != nil {
		t.Fatal(err)
	}
	tiles := fixtureRegion(root).compound["TileEntities"].list
	tiles[0], tiles[1] = tiles[1], tiles[0]
	var out bytes.Buffer
	gz, err := gzip.NewWriterLevel(&out, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	w := nbtWriter{w: gz}
	w.number(10, 1)
	w.string("different root name")
	reverseCompoundNBT(&w, root)
	if w.err != nil {
		t.Fatal(w.err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(out.Bytes()), ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	assertDenseVolume(t, got, want)
}

func TestDenseCompleteCatalogSerialization(t *testing.T) {
	want := denseFixture(alphabet.States())
	var out bytes.Buffer
	if err := Write(&out, ".litematic", want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(out.Bytes()), ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	assertDenseVolume(t, got, want)
	if err := Write(&bytes.Buffer{}, ".schematic", want); err == nil {
		t.Fatal("wrote dense legacy schematic")
	}
}

func denseBannerFixture(t *testing.T) volume.Volume {
	t.Helper()
	states := []volume.State{
		{Name: "minecraft:air", Properties: map[string]string{}},
		{Name: "minecraft:cave_air", Properties: map[string]string{}},
		{Name: "minecraft:void_air", Properties: map[string]string{}},
		{Name: "minecraft:red_banner", Properties: map[string]string{"rotation": "13"}},
		{Name: "minecraft:blue_wall_banner", Properties: map[string]string{"facing": "west"}},
	}
	v := denseFixture(states)
	v.X, v.Y, v.Z = 1, 1, len(v.Blocks)
	patterns := alphabet.Patterns()
	v.Banners[21] = []volume.Layer{
		{Pattern: patterns[0], Color: 15}, {Pattern: patterns[len(patterns)-1], Color: 0},
		{Pattern: patterns[0], Color: 1}, {Pattern: patterns[0], Color: 1},
		{Pattern: patterns[len(patterns)-1], Color: 7}, {Pattern: patterns[0], Color: 4},
	}
	v.Banners[22] = []volume.Layer{}
	return v
}

func TestDenseBannersAirTranslationAndPaletteRemapping(t *testing.T) {
	want := denseBannerFixture(t)
	for mask := range 8 {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			root, err := denseLitematicRoot(want)
			if err != nil {
				t.Fatal(err)
			}
			region := fixtureRegion(root)
			dims := [3]int{want.X, want.Y, want.Z}
			for i := range dims {
				if mask&(1<<i) != 0 {
					dims[i] = -dims[i]
				}
			}
			region.compound["Position"] = vector(100, -200, 300)
			region.compound["Size"] = vector(dims[0], dims[1], dims[2])
			root.compound["Metadata"] = compound(map[string]tag{"Anything": text("irrelevant")})
			palette := region.compound["BlockStatePalette"]
			for i := range len(palette.list) / 2 {
				j := len(palette.list) - 1 - i
				palette.list[i], palette.list[j] = palette.list[j], palette.list[i]
			}
			// This fixture has 21 palette entries, so five bits cross words.
			packed := make([]byte, ((len(want.Blocks)*5+63)/64)*8)
			for i, index := range want.Blocks {
				remapped := uint64(len(palette.list) - 1 - int(index))
				for b := range 5 {
					if remapped&(1<<b) != 0 {
						bit := i*5 + b
						word := bit / 64
						value := binary.BigEndian.Uint64(packed[word*8:]) | uint64(1)<<uint(bit%64)
						binary.BigEndian.PutUint64(packed[word*8:], value)
					}
				}
			}
			region.compound["BlockStates"] = tag{kind: 12, raw: packed}
			got, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic")
			if err != nil {
				t.Fatal(err)
			}
			assertDenseVolume(t, got, want)
		})
	}
}

func TestDenseMalformedBannerRejection(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tag)
	}{
		{name: "missing", mutate: func(r tag) {
			tiles := r.compound["TileEntities"]
			tiles.list = tiles.list[:1]
			r.compound["TileEntities"] = tiles
		}},
		{name: "duplicate", mutate: func(r tag) {
			r.compound["TileEntities"].list[1] = r.compound["TileEntities"].list[0]
		}},
		{name: "outside", mutate: func(r tag) { r.compound["TileEntities"].list[0].compound["x"] = integer(1) }},
		{name: "translated coordinates", mutate: func(r tag) {
			r.compound["Position"] = vector(100, 100, 100)
			r.compound["TileEntities"].list[0].compound["x"] = integer(100)
		}},
		{name: "nonbanner position", mutate: func(r tag) { r.compound["TileEntities"].list[0].compound["z"] = integer(18) }},
		{name: "unknown tile payload", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["CustomName"] = text("hidden")
		}},
		{name: "wrong id", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["id"] = text("minecraft:chest")
		}},
		{name: "missing patterns", mutate: func(r tag) { delete(r.compound["TileEntities"].list[0].compound, "Patterns") }},
		{name: "seven layers", mutate: func(r tag) {
			tile := r.compound["TileEntities"].list[0]
			p := tile.compound["Patterns"]
			p.list = append(p.list, p.list[0])
			tile.compound["Patterns"] = p
		}},
		{name: "invalid color", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["Patterns"].list[0].compound["Color"] = integer(16)
		}},
		{name: "negative color", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["Patterns"].list[0].compound["Color"] = integer(-1)
		}},
		{name: "unknown pattern", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["Patterns"].list[0].compound["Pattern"] = text("modded")
		}},
		{name: "unknown layer payload", mutate: func(r tag) {
			r.compound["TileEntities"].list[0].compound["Patterns"].list[0].compound["Hidden"] = integer(1)
		}},
		{name: "unknown palette payload", mutate: func(r tag) {
			r.compound["BlockStatePalette"].list[16].compound["Hidden"] = text("payload")
		}},
		{name: "incomplete state", mutate: func(r tag) {
			delete(r.compound["BlockStatePalette"].list[19].compound["Properties"].compound, "rotation")
		}},
		{name: "entity payload", mutate: func(r tag) {
			r.compound["Entities"] = tag{kind: 9, element: 10, list: []tag{compound(map[string]tag{"id": text("minecraft:pig")})}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := denseLitematicRoot(denseBannerFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(fixtureRegion(root))
			if _, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic"); err == nil {
				t.Fatal("accepted malformed dense banner/state data")
			}
		})
	}
}

func TestDenseUnknownEncodingVersionReachesCodec(t *testing.T) {
	for _, version := range []byte{0, 3, 255} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			want := denseFixture([]volume.State{{Name: "minecraft:air", Properties: map[string]string{}}})
			want.Blocks[16], want.Blocks[17] = uint32(version>>4), uint32(version&15)
			var out bytes.Buffer
			if err := Write(&out, ".litematic", want); err != nil {
				t.Fatal(err)
			}
			got, err := Read(bytes.NewReader(out.Bytes()), ".litematic")
			if err != nil || got.Blocks == nil {
				t.Fatalf("unknown encoding version did not reach dense codec: %v", err)
			}
		})
	}
}

func TestDenseWriteRejectsInvalidLayersAndNodeGrowth(t *testing.T) {
	want := denseBannerFixture(t)
	want.Banners[21][0].Color = 16
	var out bytes.Buffer
	if err := Write(&out, ".litematic", want); err == nil || out.Len() != 0 {
		t.Fatal("invalid color published output")
	}
	want = denseBannerFixture(t)
	want.X, want.Y, want.Z = 50000, 1, 1
	want.Blocks = make([]uint32, want.X)
	for i := range want.Blocks {
		want.Blocks[i] = 19
	}
	want.Banners = map[int][]volume.Layer{}
	for i := range want.Blocks {
		want.Banners[i] = []volume.Layer{}
	}
	if err := Write(&out, ".litematic", want); err == nil || out.Len() != 0 {
		t.Fatal("unbounded banner node growth published output")
	}
}
