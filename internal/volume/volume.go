// Package volume defines normalized concrete colors independent of schematic storage.
package volume

import "fmt"

const MaxBlocks = 64 * 1024 * 1024

// Volume owns colors in native x-fastest, then z, then y order.
// Colors 0–15 are fixed concrete nibbles, never palette indices.
type Volume struct {
	X, Y, Z int
	Colors  []byte
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

// Validate rejects incomplete selections and non-concrete colors.
func (v Volume) Validate() error {
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

// Index maps normalized coordinates to native storage order.
func (v Volume) Index(x, y, z int) int { return (y*v.Z+z)*v.X + x }
