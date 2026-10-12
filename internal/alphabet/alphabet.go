// Package alphabet pins the complete vanilla Java 1.20.1 dense symbol alphabet.
package alphabet

import (
	_ "embed"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

const (
	ordinaryCount = 24135
	bannerCount   = 320
	maxLayers     = 6
)

//go:embed blocks.tsv
var blockData string

//go:embed patterns.tsv
var patternData string

type property struct {
	name   string
	values []string
}

type block struct {
	start      int
	properties []property
	banner     bool
}

var (
	blocks       = make(map[string]block)
	states       = make([]volume.State, 0, ordinaryCount)
	banners      = make([]int, 0, bannerCount)
	bannerIndex  = make(map[int]int, bannerCount)
	patterns     = make([]string, 0, 41)
	patternIndex = make(map[string]uint64, 41)
	powers       [maxLayers + 1]uint64
	offsets      [maxLayers + 2]uint64
	layerBase    uint64
	combinations uint64
	cardinality  big.Int
)

func init() {
	for _, line := range strings.Split(strings.TrimSpace(patternData), "\n") {
		name, _, ok := strings.Cut(line, "\t")
		if !ok {
			panic("invalid embedded banner catalog")
		}
		patternIndex[name] = uint64(len(patterns))
		patterns = append(patterns, name)
	}
	layerBase = uint64(len(patterns)) * 16
	powers[0] = 1
	for length := 1; length <= maxLayers; length++ {
		powers[length] = powers[length-1] * layerBase
		offsets[length+1] = offsets[length] + powers[length]
	}
	combinations = offsets[maxLayers+1]
	for _, line := range strings.Split(strings.TrimSpace(blockData), "\n") {
		fields := strings.Split(line, "\t")
		name := fields[0]
		b := block{start: len(states), properties: make([]property, 0, len(fields)-1)}
		b.banner = strings.HasSuffix(name, "_banner")
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				panic("invalid embedded block catalog")
			}
			b.properties = append(b.properties, property{name: key, values: strings.Split(value, ",")})
		}
		blocks[name] = b
		expand(name, b.properties, make(map[string]string, len(b.properties)))
		if b.banner {
			for index := b.start; index < len(states); index++ {
				bannerIndex[index] = len(banners)
				banners = append(banners, index)
			}
		}
	}
	if len(states) != ordinaryCount || len(banners) != bannerCount || len(patterns) != 41 {
		panic("embedded Java 1.20.1 catalog cardinality changed")
	}
	cardinality.SetUint64(combinations)
	cardinality.Mul(&cardinality, big.NewInt(bannerCount))
	cardinality.Add(&cardinality, big.NewInt(ordinaryCount))
}

func expand(name string, properties []property, values map[string]string) {
	if len(properties) == 0 {
		copyValues := make(map[string]string, len(values))
		for key, value := range values {
			copyValues[key] = value
		}
		states = append(states, volume.State{Name: name, Properties: copyValues})
		return
	}
	p := properties[0]
	for _, value := range p.values {
		values[p.name] = value
		expand(name, properties[1:], values)
	}
}

// Count returns a fresh integer: callers cannot mutate the pinned cardinality.
func Count() *big.Int { return new(big.Int).Set(&cardinality) }

// States returns complete ordinary states in canonical tuple order. The slice
// and its property maps are shared immutable catalog data; callers must not edit them.
func States() []volume.State { return states }

// Patterns returns native 1.20.1 Pattern strings in lexicographic order.
// The returned slice is shared immutable catalog data.
func Patterns() []string { return patterns }

// IsBanner reports whether state is a complete registered standing or wall banner.
func IsBanner(state volume.State) bool {
	b, ok := blocks[state.Name]
	if !ok || !b.banner {
		return false
	}
	_, err := ordinaryIndex(state)
	return err == nil
}

// Rank validates a complete state and zero through six native banner layers.
// Unpatterned banners use their ordinary-state rank exactly once. Layers are
// big-endian base-(16*pattern count) digits; repeated or obscured layers remain distinct.
func Rank(state volume.State, layers []volume.Layer) (*big.Int, error) {
	index, err := ordinaryIndex(state)
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return big.NewInt(int64(index)), nil
	}
	banner, ok := bannerIndex[index]
	if !ok {
		return nil, fmt.Errorf("patterns on non-banner state %q", state.Name)
	}
	if len(layers) > maxLayers {
		return nil, fmt.Errorf("banner has %d layers, maximum is %d", len(layers), maxLayers)
	}
	var sequence uint64
	for i, layer := range layers {
		pattern, ok := patternIndex[layer.Pattern]
		if !ok {
			return nil, fmt.Errorf("banner layer %d has unknown native pattern %q", i, layer.Pattern)
		}
		if layer.Color < 0 || layer.Color > 15 {
			return nil, fmt.Errorf("banner layer %d has color %d outside 0..15", i, layer.Color)
		}
		// Six base-656 digits and their length offset fit uint64 (under 2^57).
		sequence = sequence*layerBase + pattern*16 + uint64(layer.Color)
	}
	rank := new(big.Int).SetUint64(combinations)
	rank.Mul(rank, big.NewInt(int64(banner)))
	var tail big.Int
	tail.SetUint64(uint64(ordinaryCount) + offsets[len(layers)] + sequence)
	rank.Add(rank, &tail)
	return rank, nil
}

// Unrank rejects nil, negative, and out-of-alphabet ranks without changing rank.
// Returned state properties are shared immutable catalog data, as with States.
// Returned layers are newly allocated and may be edited by the caller.
func Unrank(rank *big.Int) (volume.State, []volume.Layer, error) {
	if rank == nil || rank.Sign() < 0 {
		return volume.State{}, nil, fmt.Errorf("symbol rank must be nonnegative")
	}
	if rank.Cmp(&cardinality) >= 0 {
		return volume.State{}, nil, fmt.Errorf("symbol rank is outside the Java 1.20.1 alphabet")
	}
	if rank.IsUint64() && rank.Uint64() < ordinaryCount {
		return states[int(rank.Uint64())], nil, nil
	}
	var relative, divisor, banner, remainder big.Int
	relative.Sub(rank, big.NewInt(ordinaryCount))
	divisor.SetUint64(combinations)
	banner.QuoRem(&relative, &divisor, &remainder)
	sequence := remainder.Uint64()
	length := 1
	for length < maxLayers && sequence >= offsets[length+1] {
		length++
	}
	sequence -= offsets[length]
	layers := make([]volume.Layer, length)
	for i := length - 1; i >= 0; i-- {
		digit := sequence % layerBase
		sequence /= layerBase
		layers[i] = volume.Layer{Pattern: patterns[digit/16], Color: int32(digit % 16)}
	}
	return states[banners[int(banner.Int64())]], layers, nil
}

func ordinaryIndex(state volume.State) (int, error) {
	b, ok := blocks[state.Name]
	if !ok {
		return 0, fmt.Errorf("unknown Java 1.20.1 block %q", state.Name)
	}
	if len(state.Properties) != len(b.properties) {
		return 0, fmt.Errorf("block %q requires %d complete properties", state.Name, len(b.properties))
	}
	index := 0
	for _, p := range b.properties {
		value, ok := state.Properties[p.name]
		if !ok {
			return 0, fmt.Errorf("block %q lacks property %q", state.Name, p.name)
		}
		digit, ok := slices.BinarySearch(p.values, value)
		if !ok {
			return 0, fmt.Errorf("block %q property %q has unregistered value %q", state.Name, p.name, value)
		}
		index = index*len(p.values) + digit
	}
	return b.start + index, nil
}
