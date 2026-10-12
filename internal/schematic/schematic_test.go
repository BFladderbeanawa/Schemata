package schematic

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func sampleVolume(t *testing.T) volume.Volume {
	t.Helper()
	v := volume.Volume{X: 16, Y: 32, Z: 48, Colors: make([]byte, 16*32*48)}
	for y := range v.Y {
		for z := range v.Z {
			for x := range v.X {
				v.Colors[v.Index(x, y, z)] = byte((x + 3*y + 5*z) % 16)
			}
		}
	}
	return v
}

func fixtureNBT(t *testing.T, name string, root tag) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	w := nbtWriter{w: gz}
	w.named(name, root)
	if w.err != nil {
		t.Fatal(w.err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func fixtureRegion(root tag) tag { return root.compound["Regions"].compound["Schemata"] }

func assertVolume(t *testing.T, got, want volume.Volume) {
	t.Helper()
	if got.X != want.X || got.Y != want.Y || got.Z != want.Z {
		t.Fatalf("dimensions: got %dx%dx%d", got.X, got.Y, got.Z)
	}
	for y := range want.Y {
		for z := range want.Z {
			for x := range want.X {
				i := want.Index(x, y, z)
				if got.Colors[i] != want.Colors[i] {
					t.Fatalf("color at (%d,%d,%d): got %d want %d", x, y, z, got.Colors[i], want.Colors[i])
				}
			}
		}
	}
}

func TestReadPaletteRemappingAndWordCrossing(t *testing.T) {
	want := sampleVolume(t)
	root := litematicRoot(want)
	region := fixtureRegion(root)
	palette := region.compound["BlockStatePalette"]
	// Reverse concrete entries while retaining unused air. Encode independently
	// one bit at a time, including entries beginning at bits 60 through 63.
	for i := 1; i <= 8; i++ {
		palette.list[i], palette.list[17-i] = palette.list[17-i], palette.list[i]
	}
	packed := make([]byte, ((len(want.Colors)*5+63)/64)*8)
	for i, color := range want.Colors {
		index := uint64(16 - color)
		for b := range 5 {
			if index&(1<<b) == 0 {
				continue
			}
			bit := i*5 + b
			word := bit / 64
			n := binary.BigEndian.Uint64(packed[word*8:]) | uint64(1)<<uint(bit%64)
			binary.BigEndian.PutUint64(packed[word*8:], n)
		}
	}
	region.compound["BlockStates"] = tag{kind: 12, raw: packed}
	got, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".LITEMATIC")
	if err != nil {
		t.Fatal(err)
	}
	assertVolume(t, got, want)
}

func TestReadSignedSelectionTranslation(t *testing.T) {
	want := sampleVolume(t)
	for mask := range 8 {
		t.Run(string(rune('0'+mask)), func(t *testing.T) {
			root := litematicRoot(want)
			region := fixtureRegion(root)
			dims := [3]int{want.X, want.Y, want.Z}
			for i := range dims {
				if mask&(1<<i) != 0 {
					dims[i] = -dims[i]
				}
			}
			region.compound["Size"] = vector(dims[0], dims[1], dims[2])
			region.compound["Position"] = vector(-1000, 700, 3000)
			got, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic")
			if err != nil {
				t.Fatal(err)
			}
			assertVolume(t, got, want)
		})
	}
}

func TestReadLitematicBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tag)
	}{
		{name: "multiple regions", mutate: func(root tag) { root.compound["Regions"].compound["second"] = fixtureRegion(root) }},
		{name: "no regions", mutate: func(root tag) { delete(root.compound["Regions"].compound, "Schemata") }},
		{name: "version five", mutate: func(root tag) { root.compound["Version"] = integer(5) }},
		{name: "future subversion", mutate: func(root tag) { root.compound["SubVersion"] = integer(2) }},
		{name: "wrong version type", mutate: func(root tag) { root.compound["Version"] = text("6") }},
		{name: "selected air", mutate: func(root tag) { s := fixtureRegion(root).compound["BlockStates"]; s.raw[7] &= 224 }},
		{name: "unknown selected name", mutate: func(root tag) {
			fixtureRegion(root).compound["BlockStatePalette"].list[1].compound["Name"] = text("minecraft:stone")
		}},
		{name: "concrete properties", mutate: func(root tag) {
			fixtureRegion(root).compound["BlockStatePalette"].list[1].compound["Properties"] = compound(map[string]tag{"waterlogged": text("false")})
		}},
		{name: "palette index bounds", mutate: func(root tag) { s := fixtureRegion(root).compound["BlockStates"]; s.raw[7] |= 31 }},
		{name: "truncated packed words", mutate: func(root tag) {
			r := fixtureRegion(root)
			s := r.compound["BlockStates"]
			s.raw = s.raw[:len(s.raw)-8]
			r.compound["BlockStates"] = s
		}},
		{name: "wrong position type", mutate: func(root tag) { fixtureRegion(root).compound["Position"] = integer(0) }},
		{name: "zero dimension", mutate: func(root tag) { fixtureRegion(root).compound["Size"] = vector(0, 16, 16) }},
		{name: "minimum int dimension", mutate: func(root tag) { fixtureRegion(root).compound["Size"] = vector(-2147483648, 16, 16) }},
		{name: "resource dimension", mutate: func(root tag) { fixtureRegion(root).compound["Size"] = vector(67108864, 16, 16) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := litematicRoot(sampleVolume(t))
			tc.mutate(root)
			if _, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic"); err == nil {
				t.Fatal("accepted invalid selection")
			}
		})
	}
	for _, sub := range []int{-1, 0, 1} {
		t.Run("supported subversion "+string(rune('1'+sub)), func(t *testing.T) {
			want := sampleVolume(t)
			root := litematicRoot(want)
			if sub == -1 {
				delete(root.compound, "SubVersion")
			} else {
				root.compound["SubVersion"] = integer(sub)
			}
			root.compound["Optional"] = compound(map[string]tag{"extra": text("ignored")})
			got, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic")
			if err != nil {
				t.Fatal(err)
			}
			assertVolume(t, got, want)
		})
	}
}

func TestReadLegacyBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tag)
	}{
		{name: "materials", mutate: func(root tag) { root.compound["Materials"] = text("Classic") }},
		{name: "explicit version", mutate: func(root tag) { root.compound["Version"] = integer(1) }},
		{name: "other block", mutate: func(root tag) { root.compound["Blocks"].raw[0] = 1 }},
		{name: "metadata sixteen", mutate: func(root tag) { root.compound["Data"].raw[0] = 16 }},
		{name: "high block id", mutate: func(root tag) {
			add := make([]byte, len(root.compound["Blocks"].raw)/2)
			add[0] = 16
			root.compound["AddBlocks"] = tag{kind: 7, raw: add}
		}},
		{name: "short AddBlocks", mutate: func(root tag) { root.compound["AddBlocks"] = tag{kind: 7, raw: []byte{0}} }},
		{name: "short blocks", mutate: func(root tag) {
			b := root.compound["Blocks"]
			b.raw = b.raw[:len(b.raw)-1]
			root.compound["Blocks"] = b
		}},
		{name: "extra data", mutate: func(root tag) { b := root.compound["Data"]; b.raw = append(b.raw, 0); root.compound["Data"] = b }},
		{name: "negative dimension", mutate: func(root tag) { root.compound["Width"] = tag{kind: 2, num: -16} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := legacyRoot(sampleVolume(t))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(root)
			if _, err := Read(bytes.NewReader(fixtureNBT(t, "Schematic", root)), ".schematic"); err == nil {
				t.Fatal("accepted invalid legacy selection")
			}
		})
	}
	want := volume.Volume{X: 16, Y: 400, Z: 16, Colors: make([]byte, 16*400*16)}
	for i := range want.Colors {
		want.Colors[i] = byte(i % 16)
	}
	root, err := legacyRoot(want)
	if err != nil {
		t.Fatal(err)
	}
	root.compound["AddBlocks"] = tag{kind: 7, raw: make([]byte, len(want.Colors)/2)}
	got, err := Read(bytes.NewReader(fixtureNBT(t, "Schematic", root)), ".SCHEMATIC")
	if err != nil {
		t.Fatal(err)
	}
	assertVolume(t, got, want)
	if _, err := Read(bytes.NewReader(fixtureNBT(t, "wrong", root)), ".schematic"); err == nil {
		t.Fatal("accepted wrong root")
	}
}

func TestWriteCoordinatesAndPinnedVersions(t *testing.T) {
	for _, ext := range []string{".litematic", ".schematic"} {
		t.Run(ext, func(t *testing.T) {
			want := sampleVolume(t)
			var out bytes.Buffer
			if err := Write(&out, ext, want); err != nil {
				t.Fatal(err)
			}
			got, err := Read(bytes.NewReader(out.Bytes()), ext)
			if err != nil {
				t.Fatal(err)
			}
			assertVolume(t, got, want)
			gz, err := gzip.NewReader(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(gz)
			if err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			_, root, err := parseNBT(data)
			if err != nil {
				t.Fatal(err)
			}
			if ext == ".litematic" {
				for key, want := range map[string]int64{"Version": 6, "SubVersion": 1, "MinecraftDataVersion": 3465} {
					if root.compound[key].num != want {
						t.Fatalf("wrong %s", key)
					}
				}
				r := fixtureRegion(root)
				if len(r.compound["BlockStatePalette"].list) != 17 {
					t.Fatal("wrong palette size")
				}
				if r.compound["BlockStatePalette"].list[0].compound["Name"].text != "minecraft:air" {
					t.Fatal("missing unused air")
				}
				for _, key := range []string{"Entities", "TileEntities", "PendingBlockTicks", "PendingFluidTicks"} {
					if r.compound[key].kind != 9 || len(r.compound[key].list) != 0 {
						t.Fatalf("missing empty %s", key)
					}
				}
			}
		})
	}
	for _, ext := range []string{".schem", "litematic", ".nbt", ""} {
		if _, err := Read(bytes.NewReader([]byte{}), ext); err == nil {
			t.Fatalf("accepted extension %q", ext)
		}
		if err := Write(io.Discard, ext, sampleVolume(t)); err == nil {
			t.Fatalf("wrote extension %q", ext)
		}
	}
}

func TestReadMalformedNBTLengthsAndDepth(t *testing.T) {
	// Raw wire fixtures avoid relying on the writer to construct invalid NBT.
	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "negative byte array", raw: []byte{10, 0, 0, 7, 0, 0, 255, 255, 255, 255, 0}},
		{name: "oversized byte array", raw: []byte{10, 0, 0, 7, 0, 0, 127, 255, 255, 255, 0}},
		{name: "oversized long array", raw: []byte{10, 0, 0, 12, 0, 0, 127, 255, 255, 255, 0}},
		{name: "negative list", raw: []byte{10, 0, 0, 9, 0, 0, 10, 255, 255, 255, 255, 0}},
		{name: "oversized list", raw: []byte{10, 0, 0, 9, 0, 0, 10, 127, 255, 255, 255, 0}},
		{name: "end list with elements", raw: []byte{10, 0, 0, 9, 0, 0, 0, 0, 0, 0, 1, 0, 0}},
		{name: "truncated string", raw: []byte{10, 0, 0, 8, 0, 0, 255, 255, 0}},
		{name: "invalid type", raw: []byte{10, 0, 0, 13, 0, 0, 0}},
	}
	deep := []byte{10, 0, 0}
	for range 64 {
		deep = append(deep, 10, 0, 1, 'a')
	}
	for range 65 {
		deep = append(deep, 0)
	}
	cases = append(cases, struct {
		name string
		raw  []byte
	}{name: "depth limit", raw: deep})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			gz := gzip.NewWriter(&out)
			if _, err := gz.Write(tc.raw); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(bytes.NewReader(out.Bytes()), ".litematic"); err == nil {
				t.Fatal("accepted malformed NBT")
			}
		})
	}
}

func TestReadExternalLitemapyFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/litemapy-Subversion.litematic")
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, root, err := parseNBT(raw)
	if err != nil {
		t.Fatalf("external well-formed NBT rejected: %v", err)
	}
	if root.kind != 10 {
		t.Fatal("external root is not a compound")
	}
	// This authentic upstream file is not a solid concrete storage selection.
	if _, err := Read(bytes.NewReader(data), ".litematic"); err == nil {
		t.Fatal("accepted external non-storage selection")
	}
}

func TestReadNodeBudgetAndGzipIntegrity(t *testing.T) {
	// The wire payload fits the byte limit but its list exceeds the node budget.
	raw := []byte{10, 0, 0, 9, 0, 1, 'a', 1, 0, 4, 0, 0}
	raw = append(raw, make([]byte, 262144)...)
	raw = append(raw, 0)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bytes.NewReader(out.Bytes()), ".litematic"); err == nil {
		t.Fatal("accepted excessive node count")
	}
	out.Reset()
	if err := Write(&out, ".litematic", sampleVolume(t)); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(out.Bytes())
	corrupt[len(corrupt)-8] ^= 1
	if _, err := Read(bytes.NewReader(corrupt), ".litematic"); err == nil {
		t.Fatal("accepted invalid gzip checksum")
	}
	if _, err := Read(bytes.NewReader(out.Bytes()[:out.Len()-1]), ".litematic"); err == nil {
		t.Fatal("accepted truncated gzip trailer")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriteFailureAndInvalidVolume(t *testing.T) {
	if err := Write(failingWriter{}, ".litematic", sampleVolume(t)); err == nil {
		t.Fatal("discarded destination write error")
	}
	v := sampleVolume(t)
	v.Colors[0] = 16
	if err := Write(io.Discard, ".schematic", v); err == nil {
		t.Fatal("wrote invalid concrete color")
	}
	v = volume.Volume{X: 32768, Y: 16, Z: 16, Colors: make([]byte, 32768*16*16)}
	if err := Write(io.Discard, ".schematic", v); err == nil {
		t.Fatal("wrote overflowing legacy dimension")
	}
}
