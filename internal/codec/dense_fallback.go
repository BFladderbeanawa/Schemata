package codec

import (
	"fmt"
	"math/big"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
)

const denseBannerBudget = 262144 - 178659 - 128

type fallbackAlphabet struct {
	geometry chunkGeometry
	ranks    []int
	indices  []int
}

func ordinaryFallback() fallbackAlphabet {
	states := alphabet.States()
	f := fallbackAlphabet{ranks: make([]int, 0, len(states)), indices: make([]int, len(states))}
	for rank, state := range states {
		f.indices[rank] = -1
		if !alphabet.IsBanner(state) {
			f.indices[rank] = len(f.ranks)
			f.ranks = append(f.ranks, rank)
		}
	}
	f.geometry = radixGeometry(big.NewInt(int64(len(f.ranks))))
	return f
}

type denseChunk struct {
	position int
	fallback bool
}

func planDenseChunks(
	payload []byte,
	name string,
	full chunkGeometry,
	fallback fallbackAlphabet,
	banners bool,
) ([]denseChunk, int, error) {
	length := len(name) + len(payload)
	chunks := (length + denseChunkBytes - 1) / denseChunkBytes
	plan := make([]denseChunk, chunks)
	position := denseBootstrap
	remaining := denseBannerBudget
	useFallback := !banners
	var data [denseChunkBytes]byte
	for index := range chunks {
		start, size, _ := full.chunk(length, index)
		if !useFallback {
			for offset := range size {
				logical := start + offset
				if logical < len(name) {
					data[offset] = name[logical]
				} else {
					data[offset] = payload[logical-len(name)]
				}
			}
			cost := 0
			for _, rank := range bytesToSymbols(data[:size], full) {
				state, layers, err := alphabet.Unrank(rank)
				if err != nil {
					return nil, 0, fmt.Errorf("plan dense chunk %d: %w", index, err)
				}
				if alphabet.IsBanner(state) {
					cost += 7 + 3*len(layers)
				}
			}
			if cost > remaining {
				useFallback = true
			} else {
				remaining -= cost
			}
		}
		geometry := full
		if useFallback {
			geometry = fallback.geometry
		}
		plan[index] = denseChunk{position: position, fallback: useFallback}
		position += 1 + geometry.digits[size]
	}
	return plan, position, nil
}

func scanDenseChunks(length, capacity int, version byte, rankAt func(int) *big.Int) ([]denseChunk, int, error) {
	full := denseGeometry()
	fallback := ordinaryFallback()
	chunks := (length + denseChunkBytes - 1) / denseChunkBytes
	// Reject impossible capacities before allocating an index for declared chunks.
	minimum := full.positions(length)
	if version == denseVersion {
		minimum += chunks
	}
	if minimum > capacity {
		return nil, 0, fmt.Errorf("truncated dense chunks or payload")
	}
	plan := make([]denseChunk, chunks)
	position := denseBootstrap
	for index := range chunks {
		_, size, _ := full.chunk(length, index)
		geometry := full
		selected := denseChunk{position: position}
		if version == denseVersion {
			if position >= capacity {
				return nil, 0, fmt.Errorf("truncated dense chunk marker")
			}
			marker := rankAt(position)
			if !marker.IsInt64() || marker.Sign() < 0 || marker.Int64() > 1 {
				return nil, 0, fmt.Errorf("invalid dense chunk marker at chunk %d", index)
			}
			selected.fallback = marker.Int64() == 1
			position++
			if selected.fallback {
				geometry = fallback.geometry
			}
		}
		if geometry.digits[size] > capacity-position {
			return nil, 0, fmt.Errorf("truncated dense chunk %d", index)
		}
		selected.position = position
		plan[index] = selected
		position += geometry.digits[size]
	}
	return plan, position, nil
}
