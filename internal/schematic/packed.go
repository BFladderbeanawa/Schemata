package schematic

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// packedMapping maps palette indices to packed symbols by state identity.
// Entries that are not complete 1.20.1 states, or lie outside the packed
// palette, map to -1; valid reports whether every entry is a complete state.
func packedMapping(palette []tag) (mapping []int, valid bool) {
	mapping = make([]int, len(palette))
	valid = true
	for i, entry := range palette {
		mapping[i] = -1
		state, err := readDenseState(entry)
		if err != nil {
			valid = false
			continue
		}
		if symbol, ok, _ := alphabet.PackedSymbol(state); ok {
			mapping[i] = symbol
		}
	}
	return mapping, valid
}

// packedBootstrap reports whether the first eight positions are selected
// states whose 72 low stream bits spell SCHEMATA plus a version byte. The
// version is not checked here, so the codec can reject unsupported versions.
func packedBootstrap(mapping []int, raw []byte, n, width int) bool {
	if n < 8 {
		return false
	}
	var acc uint64
	var prefix [9]byte
	pending, out := 0, 0
	for i := range 8 {
		index := packedIndex(raw, i, width)
		if index >= uint64(len(mapping)) || mapping[index] < 0 {
			return false
		}
		acc |= uint64(mapping[index]) << pending
		for pending += volume.PackedBits; pending >= 8 && out < len(prefix); pending -= 8 {
			prefix[out] = byte(acc)
			acc >>= 8
			out++
		}
	}
	return string(prefix[:8]) == "SCHEMATA"
}

// unusedBitsZero checks the bits after used in the final LongArray word.
func unusedBitsZero(raw []byte, used int) bool {
	if used%64 == 0 || len(raw) == 0 {
		return true
	}
	return binary.BigEndian.Uint64(raw[len(raw)-8:])>>(used%64) == 0
}

func readPackedLitematic(region tag, size [3]int, palette []tag, raw []byte, mapping []int, valid bool) (volume.Volume, error) {
	n, err := volume.CountDense(size[0], size[1], size[2])
	if err != nil {
		return volume.Volume{}, err
	}
	if !valid {
		return volume.Volume{}, fmt.Errorf("packed palette contains an invalid block state")
	}
	for _, key := range []string{"TileEntities", "Entities", "PendingBlockTicks", "PendingFluidTicks"} {
		if list, ok := region.compound[key]; ok && (list.kind != 9 || len(list.list) != 0) {
			return volume.Volume{}, fmt.Errorf("packed %s must be empty", key)
		}
	}
	width := max(2, bits.Len(uint(len(palette)-1)))
	if !unusedBitsZero(raw, n*width) {
		return volume.Volume{}, fmt.Errorf("nonzero unused bits in final block-state word")
	}
	v := volume.Volume{X: size[0], Y: size[1], Z: size[2]}
	canonical := width == volume.PackedBits && len(mapping) == alphabet.PackedSymbols
	for i := 0; canonical && i < len(mapping); i++ {
		canonical = mapping[i] == i
	}
	if canonical {
		v.Packed = raw
		return v, nil
	}
	// Palette order, size, and bit width differ: remap by state identity.
	v.Packed = make([]byte, volume.PackedBytes(n))
	for i := range n {
		index := packedIndex(raw, i, width)
		if index >= uint64(len(mapping)) {
			return volume.Volume{}, fmt.Errorf("palette index out of bounds at block %d", i)
		}
		symbol := mapping[index]
		if symbol < 0 {
			return volume.Volume{}, fmt.Errorf("state outside the packed palette at block %d", i)
		}
		putPacked(v.Packed, i, uint64(symbol))
	}
	return v, nil
}
func putPacked(packed []byte, i int, value uint64) {
	bit := i * volume.PackedBits
	word, shift := bit/64, uint(bit%64)
	x := binary.BigEndian.Uint64(packed[word*8:]) | value<<shift
	binary.BigEndian.PutUint64(packed[word*8:], x)
	if shift > 64-volume.PackedBits {
		x = binary.BigEndian.Uint64(packed[(word+1)*8:]) | value>>(64-shift)
		binary.BigEndian.PutUint64(packed[(word+1)*8:], x)
	}
}

func packedLitematicRoot(v volume.Volume) tag {
	states := alphabet.PackedStates()
	palette := make([]tag, len(states))
	for i, state := range states {
		fields := map[string]tag{"Name": text(state.Name)}
		if len(state.Properties) != 0 {
			properties := make(map[string]tag, len(state.Properties))
			for key, value := range state.Properties {
				properties[key] = text(value)
			}
			fields["Properties"] = compound(properties)
		}
		palette[i] = compound(fields)
	}
	n := v.X * v.Y * v.Z
	region := compound(map[string]tag{
		"Position": vector(0, 0, 0), "Size": vector(v.X, v.Y, v.Z),
		"BlockStatePalette": {kind: 9, element: 10, list: palette}, "BlockStates": {kind: 12, raw: v.Packed},
		"TileEntities": emptyList(), "Entities": emptyList(),
		"PendingBlockTicks": emptyList(), "PendingFluidTicks": emptyList(),
	})
	metadata := compound(map[string]tag{
		"Name": text("Schemata"), "Author": text("Schemata"), "Description": text("Packed byte storage"),
		"RegionCount": integer(1), "TotalVolume": integer(n), "TotalBlocks": integer(n),
		"TimeCreated": {kind: 4}, "TimeModified": {kind: 4}, "EnclosingSize": vector(v.X, v.Y, v.Z),
	})
	return compound(map[string]tag{
		"Version": integer(6), "SubVersion": integer(1), "MinecraftDataVersion": integer(3465),
		"Metadata": metadata, "Regions": compound(map[string]tag{"Schemata": region}),
	})
}
