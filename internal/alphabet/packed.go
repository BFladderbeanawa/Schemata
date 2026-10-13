package alphabet

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// PackedSymbols is the packed version 6 palette size: 10 bits per position.
const PackedSymbols = 1024

// solid.tsv lists every 1.20.1 state with isSolidRender(EmptyBlockGetter,
// BlockPos.ZERO) true and no block entity, as probed from the pinned jar.
// packed.tsv is its first 1,024 states in canonical catalog order.
//
//go:embed solid.tsv
var solidData string

//go:embed packed.tsv
var packedData string

var (
	packedStates [PackedSymbols]volume.State
	// packedByOrdinary maps an ordinary canonical index to symbol+1; zero is absent.
	packedByOrdinary [ordinaryCount]uint16
)

func init() {
	lines := strings.Split(strings.TrimSuffix(packedData, "\n"), "\n")
	if len(lines) != PackedSymbols {
		panic("embedded packed palette size changed")
	}
	previous := -1
	for symbol, line := range lines {
		state, err := ParseStateKey(line)
		if err != nil {
			panic(err)
		}
		index, err := ordinaryIndex(state)
		if err != nil || index <= previous {
			panic("embedded packed palette is not in canonical catalog order")
		}
		previous = index
		// Share the catalog's immutable property map rather than the parsed copy.
		packedStates[symbol] = states[index]
		packedByOrdinary[index] = uint16(symbol + 1)
	}
}

// ParseStateKey parses the canonical `name[key=value,...]` fingerprint line.
func ParseStateKey(line string) (volume.State, error) {
	name, rest, ok := strings.Cut(line, "[")
	if !ok || !strings.HasSuffix(rest, "]") {
		return volume.State{}, fmt.Errorf("invalid state key %q", line)
	}
	state := volume.State{Name: name, Properties: map[string]string{}}
	if body := strings.TrimSuffix(rest, "]"); body != "" {
		for _, pair := range strings.Split(body, ",") {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				return volume.State{}, fmt.Errorf("invalid state key %q", line)
			}
			state.Properties[key] = value
		}
	}
	return state, nil
}

// SolidStates returns the committed probe output: sorted canonical state keys.
func SolidStates() []string {
	return strings.Split(strings.TrimSuffix(solidData, "\n"), "\n")
}

// PackedStates returns the packed palette in symbol order. The returned
// states share immutable catalog data; callers must not edit them.
func PackedStates() []volume.State { return packedStates[:] }

// PackedSymbol maps a complete state to its packed symbol by identity.
// It reports false for valid states outside the packed palette and an
// error for states that are not complete Java 1.20.1 states.
func PackedSymbol(state volume.State) (int, bool, error) {
	index, err := ordinaryIndex(state)
	if err != nil {
		return 0, false, err
	}
	symbol := packedByOrdinary[index]
	return int(symbol) - 1, symbol != 0, nil
}
