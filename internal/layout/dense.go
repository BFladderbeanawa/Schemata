package layout

import (
	"fmt"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// SelectDense chooses arbitrary positive block dimensions without allocating
// position data. Empty Policy selects compact; zero MaxHeight selects 64 blocks.
// requiredPositions must include all codec overhead and fit volume.MaxBlocks.
// See docs/dense-layout.md for the balance metric and deterministic tie rules.
func SelectDense(requiredPositions int, opts Options) (volume.Volume, error) {
	if requiredPositions < 1 || requiredPositions > volume.MaxBlocks {
		return volume.Volume{}, fmt.Errorf("required positions %d outside 1..%d", requiredPositions, volume.MaxBlocks)
	}
	if opts.MaxHeight == 0 {
		opts.MaxHeight = 64
	}
	heightOutsideRange := opts.MaxHeight < 16 || opts.MaxHeight > 384
	if heightOutsideRange || opts.MaxHeight%16 != 0 {
		return volume.Volume{}, fmt.Errorf("max height must be a multiple of 16 from 16 through 384")
	}
	if opts.PaddingPercent < 0 || opts.PaddingPercent > 100 {
		return volume.Volume{}, fmt.Errorf("padding percent must be from 0 through 100")
	}
	if opts.Policy == "" {
		opts.Policy = "compact"
	}
	if opts.Policy == "balanced" {
		for side := 1; ; side++ {
			y := min(side, opts.MaxHeight)
			// Even at the minimum height cap, side 2048 reaches MaxBlocks.
			// The first sufficient product therefore fits a 32-bit int.
			total := side * side * y
			if total < requiredPositions {
				continue
			}
			if total > volume.MaxBlocks {
				return volume.Volume{}, fmt.Errorf("balanced layout exceeds %d block limit", volume.MaxBlocks)
			}
			return volume.Volume{X: side, Y: y, Z: side}, nil
		}
	}
	if opts.Policy != "compact" && opts.Policy != "bounded" {
		return volume.Volume{}, fmt.Errorf("unknown layout policy %q", opts.Policy)
	}

	// A 1x1xrequiredPositions rectangle proves compact's exact minimum.
	limit := requiredPositions
	if opts.Policy == "bounded" {
		allowance := int64(requiredPositions) * int64(opts.PaddingPercent) / 100
		limit = int(min(int64(volume.MaxBlocks), int64(requiredPositions)+allowance))
	}
	best := candidate{}
	for y := 1; y <= opts.MaxHeight && y <= limit; y++ {
		// Horizontal symmetry permits x <= z. The volume bound then limits
		// x to sqrt(limit/y), rather than scanning all possible dimensions.
		for x := 1; x <= (limit/y)/x; x++ {
			area := x * y
			low := max(x, (requiredPositions+area-1)/area)
			high := limit / area
			if low > high {
				continue
			}
			// Clamping both integer neighbors of the midpoint minimizes
			// spread first and squared distance second for fixed x and y.
			for neighbor := range 2 {
				mid := (x + y + neighbor) / 2
				z := max(low, min(high, mid))
				c := newCandidate(x, y, z)
				if best.total == 0 || c.better(best, opts.Policy) {
					best = c
				}
			}
		}
	}
	return volume.Volume{X: best.x, Y: best.y, Z: best.z}, nil
}
