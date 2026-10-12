package schematic

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func packedIndex(raw []byte, i, width int) uint64 {
	bit := i * width
	word, shift := bit/64, uint(bit%64)
	index := binary.BigEndian.Uint64(raw[word*8:]) >> shift
	if int(shift)+width > 64 {
		index |= binary.BigEndian.Uint64(raw[(word+1)*8:]) << (64 - shift)
	}
	return index & (uint64(1)<<width - 1)
}

func concreteName(name string) (byte, bool) {
	for color, prefix := range colors {
		if name == "minecraft:"+prefix+"_concrete" {
			return byte(color), true
		}
	}
	return 0, false
}

func unitConcreteV1Bootstrap(palette []tag, raw []byte, size [3]int, width int) bool {
	if _, err := volume.Count(size[0], size[1], size[2]); err != nil {
		return false
	}
	var prefix [9]byte
	for i := range 18 {
		x, z := i/16, i%16
		index := packedIndex(raw, z*size[0]+x, width)
		if index >= uint64(len(palette)) {
			return false
		}
		entry := palette[index]
		name, err := field(entry, "Name", 8)
		if err != nil {
			return false
		}
		if properties, ok := entry.compound["Properties"]; ok {
			if properties.kind != 10 || len(properties.compound) != 0 {
				return false
			}
		}
		color, ok := concreteName(name.text)
		if !ok {
			return false
		}
		prefix[i/2] |= color << uint(4*(1-i%2))
	}
	return string(prefix[:8]) == "SCHEMATA" && prefix[8] == 1
}

func nativeDenseBootstrap(palette []tag, raw []byte, n, width int) bool {
	if n < 18 {
		return false
	}
	var prefix [9]byte
	for i := range 18 {
		index := packedIndex(raw, i, width)
		if index >= uint64(len(palette)) {
			return false
		}
		entry := palette[index]
		name, err := field(entry, "Name", 8)
		if err != nil {
			return false
		}
		if properties, ok := entry.compound["Properties"]; ok {
			if properties.kind != 10 || len(properties.compound) != 0 {
				return false
			}
		}
		color, ok := concreteName(name.text)
		if !ok {
			return false
		}
		prefix[i/2] |= color << uint(4*(1-i%2))
	}
	return string(prefix[:8]) == "SCHEMATA" && prefix[8] != 1
}

func readDenseState(entry tag) (volume.State, error) {
	state := volume.State{Properties: map[string]string{}}
	if entry.kind != 10 {
		return state, fmt.Errorf("invalid dense palette entry")
	}
	for key := range entry.compound {
		if key != "Name" && key != "Properties" {
			return state, fmt.Errorf("unknown dense block-state field %q", key)
		}
	}
	name, err := field(entry, "Name", 8)
	if err != nil {
		return state, err
	}
	state.Name = name.text
	if properties, ok := entry.compound["Properties"]; ok {
		if properties.kind != 10 {
			return state, fmt.Errorf("invalid dense block-state properties")
		}
		for key, value := range properties.compound {
			if value.kind != 8 {
				return state, fmt.Errorf("invalid property %q type", key)
			}
			state.Properties[key] = value.text
		}
	}
	if _, err := alphabet.Rank(state, nil); err != nil {
		return state, fmt.Errorf("invalid dense palette state: %w", err)
	}
	return state, nil
}

func readDenseLitematic(region tag, size [3]int, palette []tag, raw []byte) (volume.Volume, error) {
	n, err := volume.CountDense(size[0], size[1], size[2])
	if err != nil {
		return volume.Volume{}, err
	}
	v := volume.Volume{
		X: size[0], Y: size[1], Z: size[2],
		Palette: make([]volume.State, len(palette)), Blocks: make([]uint32, n),
		Banners: map[int][]volume.Layer{},
	}
	for i, entry := range palette {
		v.Palette[i], err = readDenseState(entry)
		if err != nil {
			return volume.Volume{}, err
		}
	}
	width := max(2, bits.Len(uint(len(palette)-1)))
	bannerCount := 0
	for i := range v.Blocks {
		index := packedIndex(raw, i, width)
		if index >= uint64(len(v.Palette)) {
			return volume.Volume{}, fmt.Errorf("palette index out of bounds at block %d", i)
		}
		v.Blocks[i] = uint32(index)
		if alphabet.IsBanner(v.Palette[index]) {
			bannerCount++
		}
	}
	tiles, err := field(region, "TileEntities", 9)
	if err != nil {
		return volume.Volume{}, err
	}
	if tiles.element != 10 || len(tiles.list) != bannerCount {
		return volume.Volume{}, fmt.Errorf("dense banner entity count does not match banner blocks")
	}
	for _, tile := range tiles.list {
		index, layers, err := readDenseBanner(tile, v)
		if err != nil {
			return volume.Volume{}, err
		}
		if _, exists := v.Banners[index]; exists {
			return volume.Volume{}, fmt.Errorf("duplicate banner entity at native index %d", index)
		}
		v.Banners[index] = layers
	}
	// Entity/tick lists cannot act as extra payload carriers. Metadata remains
	// irrelevant to the encoding, but native per-position data is canonical.
	for _, key := range []string{"Entities", "PendingBlockTicks", "PendingFluidTicks"} {
		if list, ok := region.compound[key]; ok {
			if list.kind != 9 || len(list.list) != 0 {
				return volume.Volume{}, fmt.Errorf("dense %s must be empty", key)
			}
		}
	}
	return v, nil
}

func readDenseBanner(tile tag, v volume.Volume) (int, []volume.Layer, error) {
	if tile.kind != 10 || len(tile.compound) != 5 {
		return 0, nil, fmt.Errorf("dense banner entity must contain only id, x, y, z and Patterns")
	}
	id, err := field(tile, "id", 8)
	if err != nil || id.text != "minecraft:banner" {
		return 0, nil, fmt.Errorf("invalid dense banner entity id")
	}
	var coords [3]int
	for i, axis := range []string{"x", "y", "z"} {
		value, err := field(tile, axis, 3)
		if err != nil {
			return 0, nil, err
		}
		coords[i] = int(value.num)
	}
	if coords[0] < 0 || coords[0] >= v.X || coords[1] < 0 || coords[1] >= v.Y || coords[2] < 0 || coords[2] >= v.Z {
		return 0, nil, fmt.Errorf("banner entity outside region")
	}
	index := v.Index(coords[0], coords[1], coords[2])
	state := v.Palette[v.Blocks[index]]
	if !alphabet.IsBanner(state) {
		return 0, nil, fmt.Errorf("banner entity on non-banner block %d", index)
	}
	patterns, err := field(tile, "Patterns", 9)
	if err != nil {
		return 0, nil, err
	}
	if patterns.element != 10 || len(patterns.list) > 6 {
		return 0, nil, fmt.Errorf("invalid native banner pattern list")
	}
	layers := make([]volume.Layer, len(patterns.list))
	for i, layer := range patterns.list {
		if layer.kind != 10 || len(layer.compound) != 2 {
			return 0, nil, fmt.Errorf("banner layer must contain only Pattern and Color")
		}
		pattern, err := field(layer, "Pattern", 8)
		if err != nil {
			return 0, nil, err
		}
		color, err := field(layer, "Color", 3)
		if err != nil {
			return 0, nil, err
		}
		if color.num < 0 || color.num > 15 {
			return 0, nil, fmt.Errorf("banner color out of range")
		}
		layers[i] = volume.Layer{Pattern: pattern.text, Color: int32(color.num)}
	}
	if _, err := alphabet.Rank(state, layers); err != nil {
		return 0, nil, fmt.Errorf("invalid banner data at block %d: %w", index, err)
	}
	return index, layers, nil
}

func denseLitematicRoot(v volume.Volume) (tag, error) {
	// Bound tag-tree allocation itself, before constructing per-banner maps.
	nodes := 33
	for _, state := range v.Palette {
		nodes += 3 + len(state.Properties)
		if nodes > maxNodes {
			return tag{}, fmt.Errorf("dense palette exceeds NBT node limit")
		}
		if _, err := alphabet.Rank(state, nil); err != nil {
			return tag{}, fmt.Errorf("invalid dense palette state: %w", err)
		}
	}
	for _, index := range v.Blocks {
		if alphabet.IsBanner(v.Palette[index]) {
			nodes += 6
		}
		if nodes > maxNodes {
			return tag{}, fmt.Errorf("dense banners exceed NBT node limit")
		}
	}
	for index, layers := range v.Banners {
		state := v.Palette[v.Blocks[index]]
		if _, err := alphabet.Rank(state, layers); err != nil {
			return tag{}, fmt.Errorf("invalid banner data at block %d: %w", index, err)
		}
		if !alphabet.IsBanner(state) {
			return tag{}, fmt.Errorf("banner data on non-banner block %d", index)
		}
		nodes += 3 * len(layers)
		if nodes > maxNodes {
			return tag{}, fmt.Errorf("dense patterns exceed NBT node limit")
		}
	}
	palette := make([]tag, len(v.Palette))
	for i, state := range v.Palette {
		properties := make(map[string]tag, len(state.Properties))
		for key, value := range state.Properties {
			properties[key] = text(value)
		}
		palette[i] = compound(map[string]tag{"Name": text(state.Name), "Properties": compound(properties)})
	}
	width := max(2, bits.Len(uint(len(palette)-1)))
	packed := make([]byte, ((len(v.Blocks)*width+63)/64)*8)
	tiles := []tag{}
	nonAir := 0
	for i, index := range v.Blocks {
		bit := i * width
		word, shift := bit/64, uint(bit%64)
		value := uint64(index)
		x := binary.BigEndian.Uint64(packed[word*8:]) | value<<shift
		binary.BigEndian.PutUint64(packed[word*8:], x)
		if int(shift)+width > 64 {
			x = binary.BigEndian.Uint64(packed[(word+1)*8:]) | value>>(64-shift)
			binary.BigEndian.PutUint64(packed[(word+1)*8:], x)
		}
		state := v.Palette[index]
		if state.Name != "minecraft:air" && state.Name != "minecraft:cave_air" && state.Name != "minecraft:void_air" {
			nonAir++
		}
		if !alphabet.IsBanner(state) {
			continue
		}
		layers := v.Banners[i]
		patterns := make([]tag, len(layers))
		for j, layer := range layers {
			patterns[j] = compound(map[string]tag{"Pattern": text(layer.Pattern), "Color": integer(int(layer.Color))})
		}
		tiles = append(tiles, compound(map[string]tag{
			"id": text("minecraft:banner"), "x": integer(i % v.X),
			"y": integer(i / (v.X * v.Z)), "z": integer((i / v.X) % v.Z),
			"Patterns": {kind: 9, element: 10, list: patterns},
		}))
	}
	region := compound(map[string]tag{
		"Position": vector(0, 0, 0), "Size": vector(v.X, v.Y, v.Z),
		"BlockStatePalette": {kind: 9, element: 10, list: palette}, "BlockStates": {kind: 12, raw: packed},
		"TileEntities": {kind: 9, element: 10, list: tiles}, "Entities": emptyList(),
		"PendingBlockTicks": emptyList(), "PendingFluidTicks": emptyList(),
	})
	metadata := compound(map[string]tag{
		"Name": text("Schemata"), "Author": text("Schemata"), "Description": text("Dense byte storage"),
		"RegionCount": integer(1), "TotalVolume": integer(len(v.Blocks)), "TotalBlocks": integer(nonAir),
		"TimeCreated": {kind: 4}, "TimeModified": {kind: 4}, "EnclosingSize": vector(v.X, v.Y, v.Z),
	})
	return compound(map[string]tag{
		"Version": integer(6), "SubVersion": integer(1), "MinecraftDataVersion": integer(3465),
		"Metadata": metadata, "Regions": compound(map[string]tag{"Schemata": region}),
	}), nil
}
