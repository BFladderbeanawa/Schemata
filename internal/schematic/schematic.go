// Package schematic adapts the pinned Litematica and legacy Alpha formats.
package schematic

import (
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strings"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

var colors = [...]string{
	"white", "orange", "magenta", "light_blue", "yellow", "lime", "pink", "gray",
	"light_gray", "cyan", "purple", "blue", "brown", "green", "red", "black",
}

func extension(ext string) (string, error) {
	ext = strings.ToLower(ext)
	if ext != ".litematic" && ext != ".schematic" {
		return "", fmt.Errorf("unsupported schematic extension %q", ext)
	}
	return ext, nil
}

// Read decodes a bounded gzip NBT selection, preserving native coordinates.
func Read(r io.Reader, ext string) (volume.Volume, error) {
	ext, err := extension(ext)
	if err != nil {
		return volume.Volume{}, err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("opening gzip schematic: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(gz, maxNBT+1))
	closeErr := gz.Close()
	if err != nil {
		return volume.Volume{}, fmt.Errorf("reading gzip schematic: %w", err)
	}
	if closeErr != nil {
		return volume.Volume{}, fmt.Errorf("closing gzip schematic: %w", closeErr)
	}
	if len(data) > maxNBT {
		return volume.Volume{}, fmt.Errorf("decompressed NBT exceeds 256 MiB limit")
	}
	name, root, err := parseNBT(data)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("reading NBT: %w", err)
	}
	if ext == ".schematic" {
		return readLegacy(name, root)
	}
	return readLitematic(root)
}

// Write emits a gzip NBT schematic in the pinned version of the requested format.
func Write(w io.Writer, ext string, v volume.Volume) error {
	ext, err := extension(ext)
	if err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return err
	}
	if v.Blocks != nil && ext != ".litematic" {
		return fmt.Errorf("dense encoding requires .litematic output")
	}
	var name string
	var root tag
	if ext == ".schematic" {
		name = "Schematic"
		root, err = legacyRoot(v)
	} else if v.Blocks != nil {
		root, err = denseLitematicRoot(v)
	} else {
		root = litematicRoot(v)
	}
	if err != nil {
		return err
	}
	if err := validateNBTOutput(name, root); err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	nw := nbtWriter{w: gz}
	nw.named(name, root)
	return errors.Join(nw.err, gz.Close())
}

func readVector(t tag, name string) ([3]int, error) {
	var out [3]int
	c, err := field(t, name, 10)
	if err != nil {
		return out, err
	}
	for i, axis := range []string{"x", "y", "z"} {
		n, err := field(c, axis, 3)
		if err != nil {
			return out, err
		}
		out[i] = int(n.num)
	}
	return out, nil
}

func readLitematic(root tag) (volume.Volume, error) {
	version, err := field(root, "Version", 3)
	if err != nil {
		return volume.Volume{}, err
	}
	if version.num != 6 {
		return volume.Volume{}, fmt.Errorf("unsupported Litematica version %d", version.num)
	}
	if sub, ok := root.compound["SubVersion"]; ok {
		if sub.kind != 3 || sub.num < 0 || sub.num > 1 {
			return volume.Volume{}, fmt.Errorf("unsupported Litematica subversion")
		}
	}
	regions, err := field(root, "Regions", 10)
	if err != nil {
		return volume.Volume{}, err
	}
	if len(regions.compound) != 1 {
		return volume.Volume{}, fmt.Errorf("Litematica requires exactly one region")
	}
	var region tag
	for _, r := range regions.compound {
		region = r
	}
	if region.kind != 10 {
		return volume.Volume{}, fmt.Errorf("invalid Litematica region")
	}
	pos, err := readVector(region, "Position")
	if err != nil {
		return volume.Volume{}, err
	}
	size, err := readVector(region, "Size")
	if err != nil {
		return volume.Volume{}, err
	}
	for i, n := range size {
		// Validate translated selection endpoints in wide arithmetic. The array is
		// already minimum-corner ordered even when selection direction is negative.
		end := int64(pos[i]) + int64(n)
		if n > 0 {
			end--
		} else {
			end++
		}
		if end < -2147483648 || end > 2147483647 {
			return volume.Volume{}, fmt.Errorf("region position and size overflow coordinates")
		}
		if n < 0 {
			size[i] = int(-int64(n))
		}
	}
	n, err := volume.CountDense(size[0], size[1], size[2])
	if err != nil {
		return volume.Volume{}, err
	}
	palette, err := field(region, "BlockStatePalette", 9)
	if err != nil {
		return volume.Volume{}, err
	}
	if palette.element != 10 || len(palette.list) == 0 {
		return volume.Volume{}, fmt.Errorf("invalid block state palette")
	}
	mapping := make([]int, len(palette.list))
	for i, entry := range palette.list {
		name, err := field(entry, "Name", 8)
		if err != nil {
			return volume.Volume{}, err
		}
		mapping[i] = -1
		validProperties := true
		if properties, ok := entry.compound["Properties"]; ok {
			validProperties = properties.kind == 10 && len(properties.compound) == 0
		}
		if validProperties {
			for color, prefix := range colors {
				if name.text == "minecraft:"+prefix+"_concrete" {
					mapping[i] = color
					break
				}
			}
		}
	}
	states, err := field(region, "BlockStates", 12)
	if err != nil {
		return volume.Volume{}, err
	}
	width := max(2, bits.Len(uint(len(mapping)-1)))
	words := (n*width + 63) / 64
	if len(states.raw) != words*8 {
		return volume.Volume{}, fmt.Errorf("invalid packed block state length")
	}
	// Identification uses actual concrete names, never palette indices. Dense
	// versions use native traversal; versions 1/5 keep the historical unit path.
	if !unitConcreteBootstrap(palette.list, states.raw, size, width) &&
		nativeDenseBootstrap(palette.list, states.raw, n, width) {
		return readDenseLitematic(region, size, palette.list, states.raw)
	}
	if _, err := volume.Count(size[0], size[1], size[2]); err != nil {
		return volume.Volume{}, err
	}
	v := volume.Volume{X: size[0], Y: size[1], Z: size[2], Colors: make([]byte, n)}
	mask := uint64(1)<<width - 1
	for i := range v.Colors {
		bit := i * width
		word, shift := bit/64, uint(bit%64)
		index := binary.BigEndian.Uint64(states.raw[word*8:]) >> shift
		if int(shift)+width > 64 {
			index |= binary.BigEndian.Uint64(states.raw[(word+1)*8:]) << (64 - shift)
		}
		index &= mask
		if index >= uint64(len(mapping)) {
			return volume.Volume{}, fmt.Errorf("palette index out of bounds at block %d", i)
		}
		if mapping[index] < 0 {
			return volume.Volume{}, fmt.Errorf("selected non-concrete or invalid state at block %d", i)
		}
		v.Colors[i] = byte(mapping[index])
	}
	return v, nil
}

func readLegacy(name string, root tag) (volume.Volume, error) {
	if name != "Schematic" {
		return volume.Volume{}, fmt.Errorf("legacy root must be Schematic")
	}
	if _, ok := root.compound["Version"]; ok {
		return volume.Volume{}, fmt.Errorf("versioned legacy variants are unsupported")
	}
	materials, err := field(root, "Materials", 8)
	if err != nil {
		return volume.Volume{}, err
	}
	if materials.text != "Alpha" {
		return volume.Volume{}, fmt.Errorf("legacy Materials must be Alpha")
	}
	var dims [3]int
	for i, axis := range []string{"Width", "Height", "Length"} {
		t, err := field(root, axis, 2)
		if err != nil {
			return volume.Volume{}, err
		}
		dims[i] = int(t.num)
	}
	n, err := volume.Count(dims[0], dims[1], dims[2])
	if err != nil {
		return volume.Volume{}, err
	}
	blocks, err := field(root, "Blocks", 7)
	if err != nil {
		return volume.Volume{}, err
	}
	data, err := field(root, "Data", 7)
	if err != nil {
		return volume.Volume{}, err
	}
	if len(blocks.raw) != n || len(data.raw) != n {
		return volume.Volume{}, fmt.Errorf("legacy block arrays do not match dimensions")
	}
	if add, ok := root.compound["AddBlocks"]; ok {
		if add.kind != 7 || len(add.raw) != (n+1)/2 {
			return volume.Volume{}, fmt.Errorf("invalid AddBlocks length")
		}
		for _, b := range add.raw {
			if b != 0 {
				return volume.Volume{}, fmt.Errorf("high block IDs are not concrete")
			}
		}
	}
	for i, block := range blocks.raw {
		if block != 251 || data.raw[i] > 15 {
			return volume.Volume{}, fmt.Errorf("non-concrete legacy block at index %d", i)
		}
	}
	return volume.Volume{X: dims[0], Y: dims[1], Z: dims[2], Colors: data.raw}, nil
}

func litematicRoot(v volume.Volume) tag {
	palette := make([]tag, 17)
	palette[0] = compound(map[string]tag{"Name": text("minecraft:air")})
	for i, color := range colors {
		palette[i+1] = compound(map[string]tag{"Name": text("minecraft:" + color + "_concrete")})
	}
	packed := make([]byte, ((len(v.Colors)*5+63)/64)*8)
	for i, color := range v.Colors {
		bit := i * 5
		word, shift := bit/64, uint(bit%64)
		value := uint64(color) + 1
		x := binary.BigEndian.Uint64(packed[word*8:]) | value<<shift
		binary.BigEndian.PutUint64(packed[word*8:], x)
		if shift > 59 {
			x = binary.BigEndian.Uint64(packed[(word+1)*8:]) | value>>(64-shift)
			binary.BigEndian.PutUint64(packed[(word+1)*8:], x)
		}
	}
	region := compound(map[string]tag{
		"Position": vector(0, 0, 0), "Size": vector(v.X, v.Y, v.Z),
		"BlockStatePalette": {kind: 9, element: 10, list: palette}, "BlockStates": {kind: 12, raw: packed},
		"Entities": emptyList(), "TileEntities": emptyList(),
		"PendingBlockTicks": emptyList(), "PendingFluidTicks": emptyList(),
	})
	metadata := compound(map[string]tag{
		"Name": text("Schemata"), "Author": text("Schemata"), "Description": text("Concrete byte storage"),
		"RegionCount": integer(1), "TotalVolume": integer(len(v.Colors)), "TotalBlocks": integer(len(v.Colors)),
		"TimeCreated": {kind: 4}, "TimeModified": {kind: 4}, "EnclosingSize": vector(v.X, v.Y, v.Z),
	})
	return compound(map[string]tag{
		"Version": integer(6), "SubVersion": integer(1), "MinecraftDataVersion": integer(3465),
		"Metadata": metadata, "Regions": compound(map[string]tag{"Schemata": region}),
	})
}

func legacyRoot(v volume.Volume) (tag, error) {
	if v.X > 32767 || v.Y > 32767 || v.Z > 32767 {
		return tag{}, fmt.Errorf("legacy dimensions exceed signed-short storage")
	}
	blocks := make([]byte, len(v.Colors))
	for i := range blocks {
		blocks[i] = 251
	}
	return compound(map[string]tag{
		"Materials": text("Alpha"), "Width": {kind: 2, num: int64(v.X)},
		"Height": {kind: 2, num: int64(v.Y)}, "Length": {kind: 2, num: int64(v.Z)},
		"Blocks": {kind: 7, raw: blocks}, "Data": {kind: 7, raw: v.Colors},
		"Entities": emptyList(), "TileEntities": emptyList(),
	}), nil
}
