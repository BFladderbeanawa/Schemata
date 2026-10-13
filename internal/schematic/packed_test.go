package schematic

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func packedFixture(t *testing.T, payload []byte) volume.Volume {
	t.Helper()
	c, err := codec.NewPackedCompressor()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	method, data := c.Compress(payload)
	digest := sha256.Sum256(payload)
	h := codec.PackedHeader{
		Method: method, FullLength: uint64(len(payload)), Count: 1, PartSize: 1 << 20,
		DataLength: uint64(len(data)), Checksum: crc32.ChecksumIEEE(payload), Digest: digest,
		Group:     codec.PackedGroup("p.bin", uint64(len(payload)), 1<<20, 1, digest),
		Aggregate: codec.PackedAggregate([][32]byte{digest}),
	}
	v, err := codec.EncodePacked(context.Background(), "p.bin", h, data, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func packedRoot(t *testing.T, v volume.Volume) tag {
	t.Helper()
	var file bytes.Buffer
	if err := Write(&file, ".litematic", v); err != nil {
		t.Fatal(err)
	}
	data, err := readGzip(file.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	_, root, err := parseNBT(data)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readGzip(b []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(gz)
}
func decodeFixture(t *testing.T, root tag) (codec.PackedPart, error) {
	t.Helper()
	v, err := Read(bytes.NewReader(fixtureNBT(t, "", root)), ".litematic")
	if err != nil {
		return codec.PackedPart{}, err
	}
	return codec.DecodePacked(context.Background(), v)
}

func TestPackedSerializationPinsAndRecovery(t *testing.T) {
	t.Parallel()
	payload := append(bytes.Repeat([]byte("packed litematic "), 300), 0, 0)
	root := packedRoot(t, packedFixture(t, payload))
	region := fixtureRegion(root)
	palette := region.compound["BlockStatePalette"].list
	if len(palette) != 1024 {
		t.Fatalf("palette has %d entries, want 1024", len(palette))
	}
	if root.compound["MinecraftDataVersion"].num != 3465 || root.compound["Metadata"].compound["Description"].text != "Packed byte storage" {
		t.Fatal("pinned root fields differ")
	}
	if _, ok := palette[454].compound["Properties"]; !ok || palette[454].compound["Name"].text != "minecraft:note_block" {
		t.Fatal("symbol 454 is not the first note_block state")
	}
	part, err := decodeFixture(t, root)
	if err != nil || part.Name != "p.bin" || !bytes.Equal(part.Data, payload) {
		t.Fatalf("recovery: %v", err)
	}
}

// Reordering and pruning the palette, plus a prepended unused air entry,
// change bit width and indices but not recovery: states map by identity.
func TestPackedPaletteRemapping(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 250, 251, 252, 253}, 64)
	v := packedFixture(t, payload)
	n := v.X * v.Y * v.Z
	root := packedRoot(t, v)
	region := fixtureRegion(root)
	palette := region.compound["BlockStatePalette"].list
	used := map[uint64]bool{}
	for i := range n {
		used[packedIndex(v.Packed, i, 10)] = true
	}
	// New palette: air first, then used symbols in reverse order.
	remap := map[uint64]uint64{}
	next := []tag{compound(map[string]tag{"Name": text("minecraft:air")})}
	for symbol := len(palette) - 1; symbol >= 0; symbol-- {
		if used[uint64(symbol)] {
			remap[uint64(symbol)] = uint64(len(next))
			next = append(next, palette[symbol])
		}
	}
	width := 2
	for 1<<width < len(next) {
		width++
	}
	raw := make([]byte, (n*width+63)/64*8)
	for i := range n {
		putBits(raw, i, width, remap[packedIndex(v.Packed, i, 10)])
	}
	region.compound["BlockStatePalette"] = tag{kind: 9, element: 10, list: next}
	region.compound["BlockStates"] = tag{kind: 12, raw: raw}
	part, err := decodeFixture(t, root)
	if err != nil || !bytes.Equal(part.Data, payload) {
		t.Fatalf("remapped recovery: %v", err)
	}
}

func putBits(raw []byte, i, width int, value uint64) {
	for b := range width {
		if value>>b&1 != 0 {
			bit := i*width + b
			word := bit / 64
			raw[word*8+7-(bit%64)/8] |= 1 << (bit % 8)
		}
	}
}

func TestPackedAdapterRejections(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{7}, 1000)
	for _, tc := range []struct {
		name  string
		alter func(region tag, palette []tag)
		want  string
	}{
		{name: "outside state", alter: func(region tag, palette []tag) {
			// Padding uses symbol 0; replacing it makes padding positions unselected.
			palette[0] = compound(map[string]tag{"Name": text("minecraft:yellow_concrete")})
		}, want: "outside the packed palette"},
		{name: "invalid palette entry", alter: func(region tag, palette []tag) {
			palette[1023] = compound(map[string]tag{"Name": text("minecraft:not_a_block")})
		}, want: "invalid block state"},
		{name: "tile entity", alter: func(region tag, palette []tag) {
			region.compound["TileEntities"] = tag{kind: 9, element: 10, list: []tag{compound(map[string]tag{})}}
		}, want: "TileEntities must be empty"},
		{name: "entity", alter: func(region tag, palette []tag) {
			region.compound["Entities"] = tag{kind: 9, element: 10, list: []tag{compound(map[string]tag{})}}
		}, want: "Entities must be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := packedRoot(t, packedFixture(t, payload))
			region := fixtureRegion(root)
			tc.alter(region, region.compound["BlockStatePalette"].list)
			if _, err := decodeFixture(t, root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPackedRejectsLegacyOutput(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := Write(&out, ".schematic", packedFixture(t, []byte("x"))); err == nil || out.Len() != 0 {
		t.Fatalf("packed .schematic accepted: %v", err)
	}
}
