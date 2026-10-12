// Package layout selects rectangular dimensions without allocating block colors.
package layout

import (
	"fmt"

	"github.com/bfladderbeanawa/schemata/internal/volume"
)

const (
	maxUnits = volume.MaxBlocks / 4096
	// DefaultMaxHeight is the height cap in blocks used when MaxHeight is
	// zero. MaxHeightLimit is the largest accepted explicit cap.
	DefaultMaxHeight = 384
	MaxHeightLimit   = 384
	// legacyHeightBlocks is the greatest multiple of 16 that fits the signed
	// short Width/Height/Length fields of legacy .schematic output.
	legacyHeightBlocks = 32752
)

// Options controls layout selection. Zero MaxHeight selects DefaultMaxHeight
// blocks and empty Policy selects balanced. NoHeightLimit lifts the cap: dense
// layouts are then bounded only by the resource limit, and concrete layouts by
// legacy signed-short storage. PaddingPercent is explicit, including zero;
// callers that want the CLI default for bounded should supply 25.
type Options struct {
	MaxHeight      int
	NoHeightLimit  bool
	Policy         string
	PaddingPercent int
}

// heightCap resolves the effective height cap in blocks. Only NoHeightLimit
// selects unlimitedHeight; an explicit cap must be a multiple of 16 from 16
// through MaxHeightLimit.
func heightCap(opts Options, unlimitedHeight int) (int, error) {
	if opts.NoHeightLimit {
		return unlimitedHeight, nil
	}
	cap := opts.MaxHeight
	if cap == 0 {
		cap = DefaultMaxHeight
	}
	if cap < 16 || cap > MaxHeightLimit || cap%16 != 0 {
		return 0, fmt.Errorf("max height must be a multiple of 16 from 16 through 384")
	}
	return cap, nil
}

// balancedFit applies the balanced construction in caller units: the smallest
// side whose cube capped at height covers required. When that product would
// exceed limit, the minimum-spread rectangle within limit is selected instead,
// so balanced never rejects a representable capacity.
func balancedFit(required, limit, height int) (candidate, bool) {
	for side := 1; side <= limit; side++ {
		y := min(side, height)
		total := side * side * y
		if total < required {
			continue
		}
		if total <= limit {
			return newCandidate(side, y, side), true
		}
		break
	}
	return spreadFit(required, limit, height)
}

// spreadFit returns the minimum-spread rectangle whose volume is from required
// through limit and whose height is at most cap. Candidates are considered
// sorted as x <= y <= z: for each (x,y) the smallest feasible z minimizes both
// spread and squared distance, so only that z is evaluated. The height bound
// y <= limit/y is exhaustive because x*y*z >= y*y.
func spreadFit(required, limit, cap int) (candidate, bool) {
	best := candidate{}
	for y := 1; y <= cap && y <= limit/y; y++ {
		for x := 1; x <= y && x*y <= limit/y; x++ {
			area := x * y
			z := max(y, (required+area-1)/area)
			if z > limit/area {
				continue
			}
			c := newCandidate(x, y, z)
			if best.total == 0 || c.better(best, "balanced") {
				best = c
			}
		}
	}
	return best, best.total != 0
}

// Select chooses dimensions in blocks, leaving Colors nil.
func Select(requiredUnits int, opts Options) (volume.Volume, error) {
	if requiredUnits < 1 || requiredUnits > maxUnits {
		return volume.Volume{}, fmt.Errorf("required units %d outside 1..%d", requiredUnits, maxUnits)
	}
	capBlocks, err := heightCap(opts, legacyHeightBlocks)
	if err != nil {
		return volume.Volume{}, err
	}
	if opts.PaddingPercent < 0 || opts.PaddingPercent > 100 {
		return volume.Volume{}, fmt.Errorf("padding percent must be from 0 through 100")
	}
	if opts.Policy == "" {
		opts.Policy = "balanced"
	}
	height := capBlocks / 16
	if opts.Policy == "balanced" {
		best, ok := balancedFit(requiredUnits, maxUnits, height)
		if !ok {
			return volume.Volume{}, fmt.Errorf("balanced layout exceeds %d block limit", volume.MaxBlocks)
		}
		return dimensions(best.x, best.y, best.z), nil
	}
	if opts.Policy != "compact" && opts.Policy != "bounded" {
		return volume.Volume{}, fmt.Errorf("unknown layout policy %q", opts.Policy)
	}

	// A 1x1xrequiredUnits rectangle proves compact's minimum volume is
	// exactly requiredUnits, so no larger rectangles need enumeration.
	limit := requiredUnits
	if opts.Policy == "bounded" {
		limit = min(maxUnits, requiredUnits+requiredUnits*opts.PaddingPercent/100)
	}
	best := candidate{}
	for y := 1; y <= height && y <= limit; y++ {
		// x <= z and x*y*z <= limit imply x*x <= limit/y. This bounds
		// enumeration to fewer than 1,300 (x,y) pairs at the resource cap.
		for x := 1; x <= (limit/y)/x; x++ {
			area := x * y
			low := max(x, (requiredUnits+area-1)/area)
			high := limit / area
			if low > high {
				continue
			}
			// For fixed x,y, spread is minimized by z within [x,y] when
			// possible. Within that interval the squared metric is minimized
			// at (x+y)/2. Clamping its two integer neighbors also covers
			// intervals outside [x,y], where spread increases monotonically.
			for _, mid := range []int{(x + y) / 2, (x + y + 1) / 2} {
				z := max(low, min(high, mid))
				c := newCandidate(x, y, z)
				if best.total == 0 || c.better(best, opts.Policy) {
					best = c
				}
			}
		}
	}
	if best.total == 0 {
		return volume.Volume{}, fmt.Errorf("no layout within resource limits")
	}
	return dimensions(best.x, best.y, best.z), nil
}

func dimensions(x, y, z int) volume.Volume {
	return volume.Volume{X: x * 16, Y: y * 16, Z: z * 16}
}

type candidate struct {
	x, y, z int
	total   int
	spread  int
	squared int64
}

func newCandidate(x, y, z int) candidate {
	dxy, dyz, dzx := int64(x-y), int64(y-z), int64(z-x)
	return candidate{
		x: x, y: y, z: z,
		total:   x * y * z,
		spread:  max(x, y, z) - min(x, y, z),
		squared: dxy*dxy + dyz*dyz + dzx*dzx,
	}
}

func (c candidate) better(other candidate, policy string) bool {
	if policy == "compact" && c.total != other.total {
		return c.total < other.total
	}
	if c.spread != other.spread {
		return c.spread < other.spread
	}
	if c.squared != other.squared {
		return c.squared < other.squared
	}
	if c.total != other.total {
		return c.total < other.total
	}
	if c.y != other.y {
		return c.y > other.y
	}
	if c.x != other.x {
		return c.x < other.x
	}
	return c.z < other.z
}
