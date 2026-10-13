package alphabet_test

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func stateKey(s volume.State) string {
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + s.Properties[k]
	}
	return s.Name + "[" + strings.Join(pairs, ",") + "]"
}

// The selection is recomputed from the probe output and the full catalog,
// independently of the committed packed list the writer uses.
func TestPackedPaletteMatchesPinnedSpecification(t *testing.T) {
	t.Parallel()
	solid := alphabet.SolidStates()
	set := make(map[string]bool, len(solid))
	blocks := map[string]bool{}
	for _, line := range solid {
		set[line] = true
		blocks[strings.SplitN(line, "[", 2)[0]] = true
	}
	if len(solid) != 1989 || len(set) != 1989 || len(blocks) != 382 {
		t.Fatalf("solid states = %d (%d unique) across %d blocks, want 1989 across 382", len(solid), len(set), len(blocks))
	}
	var selected []string
	for _, s := range alphabet.States() {
		if key := stateKey(s); set[key] {
			selected = append(selected, key)
		}
	}
	if len(selected) != 1989 {
		t.Fatalf("catalog contains %d of the 1989 solid states", len(selected))
	}
	selected = selected[:alphabet.PackedSymbols]
	digest := sha256.Sum256([]byte(strings.Join(selected, "\n") + "\n"))
	if got := hex.EncodeToString(digest[:]); got != "906c19a2ae7b3aab3680d8d556e1c763227262ab151080cbd4a45708ec45ec82" {
		t.Fatalf("packed list digest = %s", got)
	}
	packed := alphabet.PackedStates()
	selectedBlocks := map[string]bool{}
	for i, s := range packed {
		if stateKey(s) != selected[i] {
			t.Fatalf("symbol %d = %s, want %s", i, stateKey(s), selected[i])
		}
		selectedBlocks[s.Name] = true
		symbol, ok, err := alphabet.PackedSymbol(s)
		if err != nil || !ok || symbol != i {
			t.Fatalf("PackedSymbol(%s) = %d, %v, %v", selected[i], symbol, ok, err)
		}
		if strings.HasPrefix(selected[i], "minecraft:note_block[") != (i >= 454) {
			t.Fatalf("note_block boundary differs at symbol %d", i)
		}
	}
	if len(selectedBlocks) != 217 {
		t.Fatalf("packed palette spans %d blocks, want 217", len(selectedBlocks))
	}
}

func TestPackedSymbolRejectsOutsideStates(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"yellow", "white", "orange", "pink", "purple", "red"} {
		_, ok, err := alphabet.PackedSymbol(volume.State{Name: "minecraft:" + name + "_concrete"})
		if err != nil || ok {
			t.Fatalf("%s concrete: selected=%v err=%v", name, ok, err)
		}
	}
	if _, ok, err := alphabet.PackedSymbol(volume.State{Name: "minecraft:air"}); err != nil || ok {
		t.Fatalf("air: selected=%v err=%v", ok, err)
	}
	if _, _, err := alphabet.PackedSymbol(volume.State{Name: "minecraft:not_a_block"}); err == nil {
		t.Fatal("unknown block accepted")
	}
}
