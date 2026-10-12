package alphabet_test

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestCount(t *testing.T) {
	t.Parallel()
	const want = "25540862200767107655"
	if got := alphabet.Count().String(); got != want {
		t.Fatalf("Count() = %s, want %s", got, want)
	}
	alphabet.Count().SetInt64(0)
	if alphabet.Count().String() != want {
		t.Fatal("Count returned mutable shared storage")
	}
}

func TestStates(t *testing.T) {
	t.Parallel()
	states := alphabet.States()
	if len(states) != 24135 {
		t.Fatalf("ordinary state count = %d", len(states))
	}
	hash := sha256.New()
	blockNames := make(map[string]bool)
	banners := 0
	for index, state := range states {
		blockNames[state.Name] = true
		if alphabet.IsBanner(state) {
			banners++
		}
		keys := make([]string, 0, len(state.Properties))
		for key := range state.Properties {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		properties := make([]string, 0, len(keys))
		for _, key := range keys {
			properties = append(properties, key+"="+state.Properties[key])
		}
		fmt.Fprintf(hash, "%s[%s]\n", state.Name, strings.Join(properties, ","))
		rank, err := alphabet.Rank(state, nil)
		if err != nil || rank == nil || rank.Cmp(big.NewInt(int64(index))) != 0 {
			t.Fatalf("ordinary state %d rank = %v, error = %v", index, rank, err)
		}
		got, layers, err := alphabet.Unrank(big.NewInt(int64(index)))
		if err != nil || len(layers) != 0 || !reflect.DeepEqual(got, state) {
			t.Fatalf("ordinary state %d unrank mismatch: %v", index, err)
		}
	}
	if len(blockNames) != 1003 || banners != 320 {
		t.Fatalf("block names = %d, banner states = %d", len(blockNames), banners)
	}
	const wantHash = "caa0de3f16bab78ef7df9f6e4350bc2df3e17f314e7f4af20f0434506c84bdcc"
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != wantHash {
		t.Fatalf("complete canonical state fingerprint = %s, want %s", got, wantHash)
	}
}

func TestPatterns(t *testing.T) {
	t.Parallel()
	want := strings.Split("b bl bo br bri bs bt bts cbo cr cre cs dls drs flo glb gra gru hh hhb "+
		"ld ls lud mc moj mr ms pig rd rs rud sc sku ss tl tr ts tt tts vh vhr", " ")
	if !slices.Equal(alphabet.Patterns(), want) {
		t.Fatalf("native pattern ordering = %v", alphabet.Patterns())
	}
}

func TestIsBanner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state volume.State
		want  bool
	}{
		{name: "standing", state: state("white_banner", "rotation", "0"), want: true},
		{name: "wall", state: state("white_wall_banner", "facing", "east"), want: true},
		{name: "missing properties", state: state("white_banner"), want: false},
		{name: "unknown rotation", state: state("white_banner", "rotation", "16"), want: false},
		{name: "ordinary", state: state("white_concrete"), want: false},
		{name: "unknown suffix", state: state("fake_banner"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := alphabet.IsBanner(test.state); got != test.want {
				t.Fatalf("IsBanner() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRankDirectionsAndAir(t *testing.T) {
	t.Parallel()
	groups := [][]volume.State{
		{state("air"), state("cave_air"), state("void_air")},
		{state("oak_log", "axis", "x"), state("oak_log", "axis", "y"), state("oak_log", "axis", "z")},
		{
			state("white_wall_banner", "facing", "east"),
			state("white_wall_banner", "facing", "north"),
			state("white_wall_banner", "facing", "south"),
			state("white_wall_banner", "facing", "west"),
		},
		{
			state("white_banner", "rotation", "0"),
			state("white_banner", "rotation", "1"),
			state("white_banner", "rotation", "10"),
			state("white_banner", "rotation", "15"),
			state("white_banner", "rotation", "2"),
			state("white_banner", "rotation", "9"),
		},
	}
	for i, group := range groups {
		t.Run(fmt.Sprintf("group_%d", i), func(t *testing.T) {
			previous := big.NewInt(-1)
			for _, s := range group {
				rank, err := alphabet.Rank(s, nil)
				if err != nil {
					t.Fatal(err)
				}
				if rank.Cmp(previous) <= 0 {
					t.Fatalf("%v rank %s is not after %s", s, rank, previous)
				}
				previous = rank
			}
		})
	}
}

func TestRankBannerOrdering(t *testing.T) {
	t.Parallel()
	bannerStates := make([]volume.State, 0, 320)
	for _, s := range alphabet.States() {
		if alphabet.IsBanner(s) {
			bannerStates = append(bannerStates, s)
		}
	}
	const perBanner = uint64(79815194377397136)
	for bannerIndex, s := range bannerStates {
		var offset uint64
		power := uint64(1)
		for length := 1; length <= 6; length++ {
			power *= 656
			for _, last := range []bool{false, true} {
				layers := make([]volume.Layer, length)
				for i := range layers {
					layers[i] = volume.Layer{Pattern: "b", Color: 0}
					if last {
						layers[i] = volume.Layer{Pattern: "vhr", Color: 15}
					}
				}
				want := new(big.Int).SetUint64(perBanner)
				want.Mul(want, big.NewInt(int64(bannerIndex)))
				tail := uint64(24135) + offset
				if last {
					tail += power - 1
				}
				want.Add(want, new(big.Int).SetUint64(tail))
				rank, err := alphabet.Rank(s, layers)
				if err != nil || rank == nil || rank.Cmp(want) != 0 {
					t.Fatalf("banner %d length %d last %v rank = %v, want %s: %v",
						bannerIndex, length, last, rank, want, err)
				}
				gotState, gotLayers, err := alphabet.Unrank(rank)
				if err != nil || !reflect.DeepEqual(gotState, s) || !slices.Equal(gotLayers, layers) {
					t.Fatalf("banner %d length %d boundary round trip: %v", bannerIndex, length, err)
				}
			}
			offset += power
		}
	}
	banner := bannerStates[0]
	tests := []struct {
		name   string
		layers []volume.Layer
		delta  int64
	}{
		{name: "base black", layers: []volume.Layer{{Pattern: "b", Color: 15}}, delta: 15},
		{name: "second pattern", layers: []volume.Layer{{Pattern: "bl", Color: 0}}, delta: 16},
		{
			name: "ordered forward", delta: 656 + 656 + 18,
			layers: []volume.Layer{{Pattern: "b", Color: 1}, {Pattern: "bl", Color: 2}},
		},
		{
			name: "ordered reverse", delta: 656 + 18*656 + 1,
			layers: []volume.Layer{{Pattern: "bl", Color: 2}, {Pattern: "b", Color: 1}},
		},
		{name: "repeated base", layers: []volume.Layer{{Pattern: "b", Color: 0}, {Pattern: "b", Color: 0}}, delta: 656},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rank, err := alphabet.Rank(banner, test.layers)
			want := big.NewInt(24135 + test.delta)
			if err != nil || rank == nil || rank.Cmp(want) != 0 {
				t.Fatalf("rank = %v, want %s: %v", rank, want, err)
			}
			gotState, gotLayers, err := alphabet.Unrank(rank)
			if err != nil || !reflect.DeepEqual(gotState, banner) || !slices.Equal(gotLayers, test.layers) {
				t.Fatalf("ordered banner round trip mismatch: %v", err)
			}
		})
	}
}

func TestRankEveryNativeLayer(t *testing.T) {
	t.Parallel()
	banner := state("white_banner", "rotation", "0")
	patterns := alphabet.Patterns()
	for patternIndex, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			for color := int32(0); color < 16; color++ {
				layers := make([]volume.Layer, 6)
				for i := range layers {
					layers[i] = volume.Layer{
						Pattern: patterns[(patternIndex+i)%len(patterns)],
						Color:   (color + int32(i)) % 16,
					}
				}
				for length := 1; length <= len(layers); length++ {
					rank, err := alphabet.Rank(banner, layers[:length])
					if err != nil {
						t.Fatal(err)
					}
					gotState, gotLayers, err := alphabet.Unrank(rank)
					if err != nil || !reflect.DeepEqual(gotState, banner) || !slices.Equal(gotLayers, layers[:length]) {
						t.Fatalf("color %d length %d native round trip mismatch: %v", color, length, err)
					}
				}
			}
		})
	}
}

func TestRankRejectsMalformedSymbols(t *testing.T) {
	t.Parallel()
	banner := state("white_banner", "rotation", "0")
	tests := []struct {
		name   string
		state  volume.State
		layers []volume.Layer
	}{
		{name: "unknown block", state: state("not_registered")},
		{name: "unqualified block", state: volume.State{Name: "air"}},
		{name: "missing property", state: state("oak_log")},
		{name: "extra property", state: state("air", "axis", "x")},
		{name: "wrong property", state: state("oak_log", "facing", "x")},
		{name: "wrong value", state: state("oak_log", "axis", "north")},
		{name: "negative rotation", state: state("white_banner", "rotation", "-1")},
		{name: "padded rotation", state: state("white_banner", "rotation", "00")},
		{name: "unknown pattern", state: banner, layers: []volume.Layer{{Pattern: "unknown", Color: 0}}},
		{name: "registry name not native hash", state: banner, layers: []volume.Layer{{Pattern: "minecraft:base", Color: 0}}},
		{name: "negative color", state: banner, layers: []volume.Layer{{Pattern: "b", Color: -1}}},
		{name: "large color", state: banner, layers: []volume.Layer{{Pattern: "b", Color: 16}}},
		{name: "too many layers", state: banner, layers: make([]volume.Layer, 7)},
		{name: "nonbanner layers", state: state("air"), layers: []volume.Layer{{Pattern: "b", Color: 0}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if rank, err := alphabet.Rank(test.state, test.layers); err == nil {
				t.Fatalf("malformed symbol accepted with rank %v", rank)
			}
		})
	}
}

func TestUnrankBoundaries(t *testing.T) {
	t.Parallel()
	ranks := []*big.Int{
		big.NewInt(0), big.NewInt(24134), big.NewInt(24135),
		new(big.Int).Sub(alphabet.Count(), big.NewInt(1)),
	}
	for _, rank := range ranks {
		original := new(big.Int).Set(rank)
		s, layers, err := alphabet.Unrank(rank)
		if err != nil {
			t.Fatal(err)
		}
		got, err := alphabet.Rank(s, layers)
		if err != nil || got == nil || got.Cmp(original) != 0 {
			t.Fatalf("boundary %s round trip = %v: %v", original, got, err)
		}
		if rank.Cmp(original) != 0 {
			t.Fatal("Unrank mutated its input")
		}
	}
	for _, rank := range []*big.Int{nil, big.NewInt(-1), alphabet.Count(), new(big.Int).Lsh(big.NewInt(1), 10000)} {
		if _, _, err := alphabet.Unrank(rank); err == nil {
			t.Fatalf("out-of-range rank accepted: %v", rank)
		}
	}
}

func state(name string, properties ...string) volume.State {
	s := volume.State{Name: "minecraft:" + name, Properties: make(map[string]string, len(properties)/2)}
	for i := 0; i < len(properties); i += 2 {
		s.Properties[properties[i]] = properties[i+1]
	}
	return s
}
