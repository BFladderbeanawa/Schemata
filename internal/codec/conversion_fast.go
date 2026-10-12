package codec

import (
	"fmt"
	"math/big"
	"math/bits"
)

const (
	ordinaryRadix     = 23815
	ordinaryRadix2    = ordinaryRadix * ordinaryRadix
	ordinaryRadix3    = ordinaryRadix2 * ordinaryRadix
	ordinaryRadix4    = ordinaryRadix3 * ordinaryRadix
	ordinaryMaxDigits = 141
)

var ordinaryDigitCounts = radixGeometry(big.NewInt(ordinaryRadix)).digits

// ordinaryBytesToDigits writes fallback alphabet indices, most significant first.
// The caller supplies exactly ordinaryDigitCounts[len(data)] digits for a chunk
// of at most denseChunkBytes bytes. Leading zero digits are part of the format.
func ordinaryBytesToDigits(data []byte, dst []uint16) {
	if len(data) > denseChunkBytes {
		panic("ordinary chunk exceeds byte limit")
	}
	if len(dst) != ordinaryDigitCounts[len(data)] {
		panic("incorrect ordinary digit buffer length")
	}
	// Little-endian limbs stay on the stack. Dividing by four radix digits at a
	// time avoids both one big.Int allocation per digit and most limb divisions.
	var limbs [denseChunkBytes / 8]uint64
	for i, value := range data {
		reverse := len(data) - 1 - i
		limbs[reverse/8] |= uint64(value) << uint(reverse%8*8)
	}
	active := (len(data) + 7) / 8
	for active > 0 && limbs[active-1] == 0 {
		active--
	}
	for end := len(dst); end > 0; {
		start := max(0, end-4)
		var remainder uint64
		for i := active - 1; i >= 0; i-- {
			limbs[i], remainder = bits.Div64(remainder, limbs[i], ordinaryRadix4)
		}
		for active > 0 && limbs[active-1] == 0 {
			active--
		}
		for i := end - 1; i >= start; i-- {
			dst[i] = uint16(remainder % ordinaryRadix)
			remainder /= ordinaryRadix
		}
		end = start
	}
}

// ordinaryDigitsToBytes validates a complete chunk and restores leading zero
// bytes. It does not modify dst on malformed or unused numeric combinations.
func ordinaryDigitsToBytes(dst []byte, digits []uint16) error {
	if len(dst) > denseChunkBytes {
		return fmt.Errorf("ordinary chunk exceeds byte limit")
	}
	if len(digits) != ordinaryDigitCounts[len(dst)] {
		return fmt.Errorf("incorrect ordinary dense symbol count")
	}
	var limbs [denseChunkBytes / 8]uint64
	var active int
	for start := 0; start < len(digits); {
		// A short most-significant group makes every later group radix^4.
		count := 4
		if start == 0 && len(digits)%4 != 0 {
			count = len(digits) % 4
		}
		var group uint64
		for _, digit := range digits[start : start+count] {
			if digit >= ordinaryRadix {
				return fmt.Errorf("invalid dense symbol rank")
			}
			group = group*ordinaryRadix + uint64(digit)
		}
		multiplier := uint64(ordinaryRadix4)
		switch count {
		case 1:
			multiplier = ordinaryRadix
		case 2:
			multiplier = ordinaryRadix2
		case 3:
			multiplier = ordinaryRadix3
		}
		carry := group
		for i := range active {
			high, low := bits.Mul64(limbs[i], multiplier)
			var overflow uint64
			limbs[i], overflow = bits.Add64(low, carry, 0)
			carry = high + overflow
		}
		if carry != 0 {
			if active == len(limbs) {
				return fmt.Errorf("unused dense chunk numeric combination")
			}
			limbs[active] = carry
			active++
		}
		start += count
	}
	if active > 0 && (active-1)*64+bits.Len64(limbs[active-1]) > len(dst)*8 {
		return fmt.Errorf("unused dense chunk numeric combination")
	}
	for i := range dst {
		reverse := len(dst) - 1 - i
		dst[i] = byte(limbs[reverse/8] >> uint(reverse%8*8))
	}
	return nil
}
