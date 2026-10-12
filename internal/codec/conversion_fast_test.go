package codec

import (
	"bytes"
	"math/big"
	"strconv"
	"testing"
)

func TestOrdinaryConversionByteExactBoundaries(t *testing.T) {
	t.Parallel()
	g := ordinaryFallback().geometry
	for size := range denseChunkBytes + 1 {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*37 + size)
			}
			if size > 0 {
				data[0] = 0
				data[size-1] = 0
			}
			if size > 2 {
				data[1] = 255
			}
			var storage [ordinaryMaxDigits]uint16
			digits := storage[:g.digits[size]]
			for i := range digits {
				digits[i] = 65535
			}
			ordinaryBytesToDigits(data, digits)
			// The established arbitrary-precision codec is an independent oracle
			// for the wire ranks, including every leading zero digit.
			want := bytesToSymbols(data, g)
			for i, rank := range want {
				if uint64(digits[i]) != rank.Uint64() {
					t.Fatalf("rank %d = %d, want %s", i, digits[i], rank)
				}
			}
			got := bytes.Repeat([]byte{0xa5}, size)
			if err := ordinaryDigitsToBytes(got, digits); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("decoded %x, want %x", got, data)
			}
		})
	}
}

func TestOrdinaryConversionNumericBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
		want []uint16
	}{
		{name: "zero", data: []byte{0}, want: []uint16{0}},
		{name: "one", data: []byte{1}, want: []uint16{1}},
		{name: "maximum byte", data: []byte{255}, want: []uint16{255}},
		{name: "leading zero byte", data: []byte{0, 255}, want: []uint16{0, 255}},
		{name: "below radix", data: []byte{0x5d, 0x06}, want: []uint16{0, 23814}},
		{name: "radix", data: []byte{0x5d, 0x07}, want: []uint16{1, 0}},
		{name: "above radix", data: []byte{0x5d, 0x08}, want: []uint16{1, 1}},
		{name: "maximum two bytes", data: []byte{255, 255}, want: []uint16{2, 17905}},
		{name: "limb boundary", data: bytes.Repeat([]byte{255}, 8)},
		{name: "maximum chunk", data: bytes.Repeat([]byte{255}, denseChunkBytes)},
		{name: "zero partial chunk", data: make([]byte, 7)},
		{name: "zero full chunk", data: make([]byte, denseChunkBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := ordinaryFallback().geometry
			digits := make([]uint16, g.digits[len(tc.data)])
			ordinaryBytesToDigits(tc.data, digits)
			if tc.want != nil {
				for i, want := range tc.want {
					if digits[i] != want {
						t.Fatalf("rank %d = %d, want %d", i, digits[i], want)
					}
				}
			}
			symbols := make([]*big.Int, len(digits))
			for i, digit := range digits {
				symbols[i] = new(big.Int).SetUint64(uint64(digit))
			}
			got := make([]byte, len(tc.data))
			if err := symbolsToBytes(got, symbols, g); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("wire ranks recover %x, want %x", got, tc.data)
			}
			clear(got)
			if err := ordinaryDigitsToBytes(got, digits); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("decoded %x, want %x", got, tc.data)
			}
		})
	}
}

func TestOrdinaryDigitsToBytesRejectsCorruption(t *testing.T) {
	t.Parallel()
	unusedFull := make([]uint16, ordinaryMaxDigits)
	for i := range unusedFull {
		unusedFull[i] = ordinaryRadix - 1
	}
	for _, tc := range []struct {
		name   string
		size   int
		digits []uint16
	}{
		{name: "rank equals radix", size: 1, digits: []uint16{ordinaryRadix}},
		{name: "rank exceeds radix", size: 1, digits: []uint16{65535}},
		{name: "first unused byte value", size: 1, digits: []uint16{256}},
		{name: "maximum unused byte value", size: 1, digits: []uint16{ordinaryRadix - 1}},
		{name: "unused partial limb", size: 2, digits: []uint16{2, 17906}},
		{name: "overflow full chunk", size: denseChunkBytes, digits: unusedFull},
		{name: "missing digits", size: 2, digits: []uint16{0}},
		{name: "extra digits", size: 1, digits: []uint16{0, 0}},
		{name: "digits for empty chunk", size: 0, digits: []uint16{0}},
		{name: "oversized chunk", size: denseChunkBytes + 1, digits: []uint16{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bytes.Repeat([]byte{0xa5}, tc.size)
			before := bytes.Clone(got)
			if err := ordinaryDigitsToBytes(got, tc.digits); err == nil {
				t.Fatal("corrupt digits accepted")
			}
			if !bytes.Equal(got, before) {
				t.Fatal("corrupt digits modified destination")
			}
		})
	}
}
