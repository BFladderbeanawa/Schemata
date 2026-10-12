// Package volume defines normalized block data independent of schematic storage.
package volume

import (
	"fmt"
	"strings"
)

const MaxBlocks = 64 * 1024 * 1024

// State is a complete native Minecraft block-state tuple.
type State struct {
	Name       string
	Properties map[string]string
}

// Layer is one native ordered banner pattern and dye color.
type Layer struct {
	Pattern string
	Color   int32
}

// Volume owns blocks in native x-fastest, then z, then y order.
// Colors 0–15 are concrete nibbles. Non-nil Blocks selects dense storage;
// its values index Palette, and Banners is keyed by native block index.
type Volume struct {
	X, Y, Z int
	Colors  []byte
	Palette []State
	Blocks  []uint32
	Banners map[int][]Layer
}

// Count validates dimensions before multiplication or allocation.
func Count(x, y, z int) (int, error) {
	for _, n := range []int{x, y, z} {
		if n <= 0 || n%16 != 0 || n > MaxBlocks {
			return 0, fmt.Errorf("dimension %d must be a positive multiple of 16 within resource limits", n)
		}
	}
	if x > MaxBlocks/y || x*y > MaxBlocks/z {
		return 0, fmt.Errorf("volume exceeds %d block limit", MaxBlocks)
	}
	return x * y * z, nil
}

// CountDense validates arbitrary positive dimensions before multiplication.
func CountDense(x, y, z int) (int, error) {
	for _, n := range []int{x, y, z} {
		if n <= 0 || n > MaxBlocks {
			return 0, fmt.Errorf("dimension %d must be positive within resource limits", n)
		}
	}
	if x > MaxBlocks/y || x*y > MaxBlocks/z {
		return 0, fmt.Errorf("volume exceeds %d block limit", MaxBlocks)
	}
	return x * y * z, nil
}

// Validate rejects incomplete selections and invalid storage references.
func (v Volume) Validate() error {
	if v.Blocks != nil {
		return v.validateDense()
	}
	n, err := Count(v.X, v.Y, v.Z)
	if err != nil {
		return err
	}
	if len(v.Colors) != n {
		return fmt.Errorf("block count %d does not match volume %d", len(v.Colors), n)
	}
	for i, c := range v.Colors {
		if c > 15 {
			return fmt.Errorf("non-concrete block at native index %d", i)
		}
	}
	return nil
}

func (v Volume) validateDense() error {
	n, err := CountDense(v.X, v.Y, v.Z)
	if err != nil {
		return err
	}
	if len(v.Colors) != 0 || len(v.Blocks) != n {
		return fmt.Errorf("dense block count or mixed concrete storage is invalid")
	}
	if len(v.Palette) == 0 || len(v.Palette) > MaxBlocks {
		return fmt.Errorf("invalid dense palette size")
	}
	for i, index := range v.Blocks {
		if uint64(index) >= uint64(len(v.Palette)) {
			return fmt.Errorf("palette index out of bounds at block %d", i)
		}
		_, hasBanner := v.Banners[i]
		isBanner := strings.HasSuffix(v.Palette[index].Name, "_banner")
		if hasBanner != isBanner {
			return fmt.Errorf("missing or inconsistent banner data at native index %d", i)
		}
	}
	for i, layers := range v.Banners {
		if i < 0 || i >= n || len(layers) > 6 {
			return fmt.Errorf("invalid banner index or layer count at block %d", i)
		}
	}
	return nil
}

// Index maps normalized coordinates to native storage order.
func (v Volume) Index(x, y, z int) int { return (y*v.Z+z)*v.X + x }
