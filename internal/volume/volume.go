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
// Non-nil Packed selects packed storage: the Litematica LongArray bytes as
// serialized (big-endian 64-bit words), 10 bits per position, least-significant
// bits first, whose values are packed palette symbols.
type Volume struct {
	X, Y, Z int
	Colors  []byte
	Palette []State
	Blocks  []uint32
	Banners map[int][]Layer
	Packed  []byte
}

// PackedBits is the packed symbol width; PackedBytes is its LongArray size.
const PackedBits = 10

// PackedBytes returns the LongArray byte length for n packed positions.
func PackedBytes(n int) int { return (n*PackedBits + 63) / 64 * 8 }

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
	if v.Packed != nil {
		return v.validatePacked()
	}
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

// validatePacked also requires the final word's unused high bits to be zero.
func (v Volume) validatePacked() error {
	n, err := CountDense(v.X, v.Y, v.Z)
	if err != nil {
		return err
	}
	if len(v.Colors) != 0 || v.Blocks != nil || len(v.Palette) != 0 || len(v.Banners) != 0 {
		return fmt.Errorf("packed storage cannot be mixed with other storage")
	}
	if len(v.Packed) != PackedBytes(n) {
		return fmt.Errorf("packed LongArray length does not match volume")
	}
	if used := n * PackedBits % 64; used != 0 {
		last := v.Packed[len(v.Packed)-8:]
		word := uint64(0)
		for _, b := range last {
			word = word<<8 | uint64(b)
		}
		if word>>used != 0 {
			return fmt.Errorf("nonzero unused bits in final packed word")
		}
	}
	return nil
}

// Index maps normalized coordinates to native storage order.
func (v Volume) Index(x, y, z int) int { return (y*v.Z+z)*v.X + x }
