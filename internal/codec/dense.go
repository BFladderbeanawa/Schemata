package codec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

const (
	denseVersion       = 3
	denseBootstrap     = prefixBytes * 2
	denseChunkBytes    = 256
	MaxDensePayload    = 32 * 1024 * 1024
	densePartVersion   = 4
	denseManifestBytes = 64
	MaxDenseParts      = 4096
)

var concreteNames = [...]string{
	"white", "orange", "magenta", "light_blue", "yellow", "lime", "pink", "gray",
	"light_gray", "cyan", "purple", "blue", "brown", "green", "red", "black",
}

// chunkGeometry is fixed by the version, never worker scheduling or dimensions.
type chunkGeometry struct {
	base   *big.Int
	digits [denseChunkBytes + 1]int
}

func denseGeometry() chunkGeometry { return radixGeometry(alphabet.Count()) }

func radixGeometry(base *big.Int) chunkGeometry {
	g := chunkGeometry{base: base}
	power := big.NewInt(1)
	limit := big.NewInt(1)
	for b := 1; b <= denseChunkBytes; b++ {
		limit.Lsh(limit, 8)
		g.digits[b] = g.digits[b-1]
		for power.Cmp(limit) < 0 {
			power.Mul(power, g.base)
			g.digits[b]++
		}
	}
	return g
}

func (g chunkGeometry) positions(length int) int {
	return denseBootstrap + length/denseChunkBytes*g.digits[denseChunkBytes] + g.digits[length%denseChunkBytes]
}

func (g chunkGeometry) chunk(length, index int) (int, int, int) {
	start := index * denseChunkBytes
	bytes := min(denseChunkBytes, length-start)
	position := denseBootstrap + index*g.digits[denseChunkBytes]
	return start, bytes, position
}

// DenseOptions controls layout and whether banner symbols are eligible.
// The zero value uses balanced layout and only nonbanner states.
type DenseOptions struct {
	Layout  layout.Options
	Banners bool
}

// EncodeDensePart writes a version 4 volume containing one canonical file part.
func EncodeDensePart(payload []byte, name string, opts DenseOptions, part PartInfo) (volume.Volume, error) {
	return EncodeDensePartContext(context.Background(), payload, name, opts, part)
}

// EncodeDensePartContext checks cancellation between bounded conversion batches.
func EncodeDensePartContext(
	ctx context.Context,
	payload []byte,
	name string,
	opts DenseOptions,
	part PartInfo,
) (volume.Volume, error) {
	if ctx == nil {
		return volume.Volume{}, fmt.Errorf("encode context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, fmt.Errorf("encode: %w", err)
	}
	if err := validateDensePart(part, uint64(len(payload))); err != nil {
		return volume.Volume{}, fmt.Errorf("encode multipart manifest: %w", err)
	}
	if err := validateDensePartChecksum(part, payload); err != nil {
		return volume.Volume{}, err
	}
	manifest := marshalDensePart(part)
	return encodeDense(ctx, payload, name, opts, manifest[:], densePartVersion)
}

func validateDensePart(part PartInfo, payloadLength uint64) error {
	if part.PartSize == 0 || part.PartSize > MaxDensePayload {
		return fmt.Errorf("part size must be from 1 through 32 MiB")
	}
	if part.Count == 0 || part.Count > MaxDenseParts {
		return fmt.Errorf("part count must be from 1 through %d", MaxDenseParts)
	}
	size := uint64(part.PartSize)
	// Check a bounded total before rounding or multiplying untrusted lengths.
	if part.TotalLength > size*MaxDenseParts {
		return fmt.Errorf("total length exceeds multipart capacity")
	}
	count := max(uint64(1), (part.TotalLength+size-1)/size)
	if uint64(part.Count) != count {
		return fmt.Errorf("part count does not match total length and part size")
	}
	if part.Index >= part.Count {
		return fmt.Errorf("part index is outside part count")
	}
	offset := uint64(part.Index) * size
	if part.Offset != offset || offset > part.TotalLength {
		return fmt.Errorf("part offset does not match index and part size")
	}
	if payloadLength != min(size, part.TotalLength-offset) {
		return fmt.Errorf("part payload length does not match manifest")
	}
	return nil
}

func validateDensePartChecksum(part PartInfo, payload []byte) error {
	if part.Count != 1 {
		return nil
	}
	if crc32.ChecksumIEEE(payload) != part.Checksum {
		return fmt.Errorf("full-file checksum mismatch")
	}
	if sha256.Sum256(payload) != part.ID {
		return fmt.Errorf("full-file SHA256 mismatch")
	}
	return nil
}

func marshalDensePart(part PartInfo) [denseManifestBytes]byte {
	var manifest [denseManifestBytes]byte
	copy(manifest[:32], part.ID[:])
	binary.BigEndian.PutUint64(manifest[32:40], part.TotalLength)
	binary.BigEndian.PutUint64(manifest[40:48], part.Offset)
	binary.BigEndian.PutUint32(manifest[48:52], part.Index)
	binary.BigEndian.PutUint32(manifest[52:56], part.Count)
	binary.BigEndian.PutUint32(manifest[56:60], part.Checksum)
	binary.BigEndian.PutUint32(manifest[60:64], part.PartSize)
	return manifest
}

func unmarshalDensePart(manifest []byte) PartInfo {
	part := PartInfo{
		TotalLength: binary.BigEndian.Uint64(manifest[32:40]),
		Offset:      binary.BigEndian.Uint64(manifest[40:48]),
		Index:       binary.BigEndian.Uint32(manifest[48:52]),
		Count:       binary.BigEndian.Uint32(manifest[52:56]),
		Checksum:    binary.BigEndian.Uint32(manifest[56:60]),
		PartSize:    binary.BigEndian.Uint32(manifest[60:64]),
	}
	copy(part.ID[:], manifest[:32])
	return part
}

// EncodeDense writes version 3 chunks, using only nonbanner states by default.
// Banners enables the full alphabet with resource-aware nonbanner fallback.
// Only .litematic can serialize the returned arbitrary-state volume.
func EncodeDense(payload []byte, name string, opts DenseOptions) (volume.Volume, error) {
	return encodeDense(context.Background(), payload, name, opts, nil, denseVersion)
}

func encodeDense(
	ctx context.Context,
	payload []byte,
	name string,
	opts DenseOptions,
	manifest []byte,
	version byte,
) (volume.Volume, error) {
	if err := validateName(name); err != nil {
		return volume.Volume{}, fmt.Errorf("encode filename: %w", err)
	}
	if len(name) > 65535 {
		return volume.Volume{}, fmt.Errorf("filename exceeds 65535 UTF-8 bytes")
	}
	if len(payload) > MaxDensePayload {
		return volume.Volume{}, fmt.Errorf("dense payload exceeds 32 MiB limit")
	}
	g := denseGeometry()
	fallback := ordinaryFallback()
	length := len(name) + len(manifest) + len(payload)
	plan, required, err := planDenseChunks(payload, name, manifest, g, fallback, opts.Banners)
	if contextErr := ctx.Err(); contextErr != nil {
		return volume.Volume{}, fmt.Errorf("encode: %w", contextErr)
	}
	if err != nil {
		return volume.Volume{}, err
	}
	v, err := layout.SelectDense(required, opts.Layout)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("select dense layout: %w", err)
	}
	n, err := volume.CountDense(v.X, v.Y, v.Z)
	if err != nil {
		return volume.Volume{}, err
	}
	v.Blocks = make([]uint32, n)
	v.Palette = make([]volume.State, 0)
	v.Banners = make(map[int][]volume.Layer)
	palette := make(map[uint64]uint32)
	put := func(position int, rank *big.Int) error {
		state, layers, err := alphabet.Unrank(rank)
		if err != nil {
			return err
		}
		ordinary, err := alphabet.Rank(state, nil)
		if err != nil {
			return err
		}
		key := ordinary.Uint64()
		index, exists := palette[key]
		if !exists {
			index = uint32(len(v.Palette))
			palette[key] = index
			v.Palette = append(v.Palette, state)
		}
		v.Blocks[position] = index
		if alphabet.IsBanner(state) {
			v.Banners[position] = layers
		}
		return nil
	}
	if err := put(0, big.NewInt(0)); err != nil {
		return volume.Volume{}, err
	}
	// Rank zero is palette index zero; zero-initialized blocks are canonical padding.
	var header [prefixBytes]byte
	copy(header[:8], magic)
	header[8] = version
	binary.BigEndian.PutUint64(header[9:17], uint64(len(payload)))
	binary.BigEndian.PutUint16(header[17:19], uint16(len(name)))
	binary.BigEndian.PutUint32(header[19:23], crc32.ChecksumIEEE(payload))
	for i, b := range header {
		for half, nibble := range []byte{b >> 4, b & 15} {
			state := volume.State{Name: "minecraft:" + concreteNames[nibble] + "_concrete"}
			rank, err := alphabet.Rank(state, nil)
			if err != nil {
				return volume.Volume{}, err
			}
			if err := put(i*2+half, rank); err != nil {
				return volume.Volume{}, err
			}
		}
	}
	chunks := (length + denseChunkBytes - 1) / denseChunkBytes
	workers := min(chunks, min(runtime.GOMAXPROCS(0), 256))
	const batchChunks = 1024
	maxDigits := fallback.geometry.digits[denseChunkBytes]
	ordinaryResults := make([]uint16, min(chunks, batchChunks)*maxDigits)
	fullResults := make([][]*big.Int, min(chunks, batchChunks))
	// Workers persist across bounded batches; compact ordinary digits avoid a
	// big.Int allocation and alphabet rank/unrank work for every nonbanner symbol.
	jobs := make(chan int, workers)
	var batchDone, workerDone sync.WaitGroup
	workerDone.Add(workers)
	for range workers {
		go func() {
			defer workerDone.Done()
			var chunk [denseChunkBytes]byte
			for index := range jobs {
				start, size, _ := g.chunk(length, index)
				fillDenseChunk(chunk[:size], start, name, manifest, payload)
				slot := index % batchChunks
				if plan[index].fallback {
					digits := ordinaryResults[slot*maxDigits : slot*maxDigits+fallback.geometry.digits[size]]
					ordinaryBytesToDigits(chunk[:size], digits)
				} else {
					fullResults[slot] = bytesToSymbols(chunk[:size], g)
				}
				batchDone.Done()
			}
		}()
	}
	defer func() { close(jobs); workerDone.Wait() }()
	ordinaryPalette := make([]uint32, len(fallback.ranks))
	for i, rank := range fallback.ranks {
		ordinaryPalette[i] = ^uint32(0)
		if index, ok := palette[uint64(rank)]; ok {
			ordinaryPalette[i] = index
		}
	}
	putOrdinary := func(position int, digit uint16) {
		index := ordinaryPalette[digit]
		if index == ^uint32(0) {
			rank := fallback.ranks[digit]
			if existing, ok := palette[uint64(rank)]; ok {
				index = existing
			} else {
				index = uint32(len(v.Palette))
				palette[uint64(rank)] = index
				v.Palette = append(v.Palette, alphabet.States()[rank])
			}
			ordinaryPalette[digit] = index
		}
		v.Blocks[position] = index
	}
	for first := 0; first < chunks; first += batchChunks {
		if err := ctx.Err(); err != nil {
			return volume.Volume{}, fmt.Errorf("encode: %w", err)
		}
		batch := min(batchChunks, chunks-first)
		batchDone.Add(batch)
		for index := first; index < first+batch; index++ {
			jobs <- index
		}
		batchDone.Wait()
		// Integrate in logical order so palette and banner output remain stable.
		for slot := range batch {
			index := first + slot
			selected := plan[index]
			if selected.fallback {
				putOrdinary(selected.position, uint16(fallback.indices[1]))
				_, size, _ := g.chunk(length, index)
				digits := ordinaryResults[slot*maxDigits : slot*maxDigits+fallback.geometry.digits[size]]
				for offset, digit := range digits {
					putOrdinary(selected.position+1+offset, digit)
				}
				continue
			}
			if err := put(selected.position, big.NewInt(0)); err != nil {
				return volume.Volume{}, err
			}
			for offset, rank := range fullResults[slot] {
				if err := put(selected.position+1+offset, rank); err != nil {
					return volume.Volume{}, err
				}
			}
			fullResults[slot] = nil
		}
	}
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, fmt.Errorf("encode: %w", err)
	}
	return v, nil
}

func bytesToSymbols(data []byte, g chunkGeometry) []*big.Int {
	digits := make([]*big.Int, g.digits[len(data)])
	value := new(big.Int).SetBytes(data)
	for i := len(digits) - 1; i >= 0; i-- {
		digits[i] = new(big.Int)
		value.QuoRem(value, g.base, digits[i])
	}
	return digits
}

func symbolsToBytes(dst []byte, symbols []*big.Int, g chunkGeometry) error {
	value := new(big.Int)
	for _, rank := range symbols {
		if rank == nil || rank.Sign() < 0 || rank.Cmp(g.base) >= 0 {
			return fmt.Errorf("invalid dense symbol rank")
		}
		value.Mul(value, g.base)
		value.Add(value, rank)
	}
	if value.BitLen() > len(dst)*8 {
		return fmt.Errorf("unused dense chunk numeric combination")
	}
	value.FillBytes(dst)
	return nil
}

func decodeDense(ctx context.Context, v volume.Volume, workers int) (File, error) {
	if err := v.Validate(); err != nil {
		return File{}, fmt.Errorf("dense structure: %w", err)
	}
	if len(v.Blocks) < denseBootstrap {
		return File{}, fmt.Errorf("truncated dense bootstrap")
	}
	var header [prefixBytes]byte
	for i := range denseBootstrap {
		state := v.Palette[v.Blocks[i]]
		nibble := -1
		if len(state.Properties) == 0 && len(v.Banners[i]) == 0 {
			for color, name := range concreteNames {
				if state.Name == "minecraft:"+name+"_concrete" {
					nibble = color
					break
				}
			}
		}
		if nibble < 0 {
			return File{}, fmt.Errorf("non-concrete dense bootstrap at position %d", i)
		}
		header[i/2] |= byte(nibble) << uint(4*(1-i%2))
	}
	if string(header[:8]) != magic {
		return File{}, fmt.Errorf("invalid SCHEMATA header")
	}
	switch header[8] {
	case 2, denseVersion, densePartVersion:
	default:
		return File{}, fmt.Errorf("unsupported encoding version %d", header[8])
	}
	payloadLength := binary.BigEndian.Uint64(header[9:17])
	nameLength := int(binary.BigEndian.Uint16(header[17:19]))
	if payloadLength > MaxDensePayload {
		return File{}, fmt.Errorf("dense payload exceeds 32 MiB limit")
	}
	if nameLength == 0 {
		return File{}, fmt.Errorf("filename must be nonempty")
	}
	manifestLength := 0
	if header[8] == densePartVersion {
		manifestLength = denseManifestBytes
	}
	length := nameLength + manifestLength + int(payloadLength)
	g := denseGeometry()
	// Rank palette states once; banner positions override the ordinary rank.
	ranks := make([]*big.Int, len(v.Palette))
	for i, state := range v.Palette {
		rank, err := alphabet.Rank(state, nil)
		if err != nil {
			return File{}, fmt.Errorf("dense palette entry %d: %w", i, err)
		}
		ranks[i] = rank
	}
	banners := make(map[int]*big.Int, len(v.Banners))
	for position, layers := range v.Banners {
		rank, err := alphabet.Rank(v.Palette[v.Blocks[position]], layers)
		if err != nil {
			return File{}, fmt.Errorf("dense banner position %d: %w", position, err)
		}
		banners[position] = rank
	}
	rankAt := func(position int) *big.Int {
		if rank, ok := banners[position]; ok {
			return rank
		}
		return ranks[v.Blocks[position]]
	}
	plan, used, err := scanDenseChunks(length, len(v.Blocks), header[8], rankAt)
	if err != nil {
		return File{}, err
	}
	fallback := ordinaryFallback()
	ordinaryDigits := make([]int, len(ranks))
	for i, rank := range ranks {
		ordinaryDigits[i] = -1
		if rank.IsInt64() && rank.Sign() >= 0 && rank.Int64() < int64(len(fallback.indices)) {
			ordinaryDigits[i] = fallback.indices[rank.Int64()]
		}
	}
	for position := used; position < len(v.Blocks); position++ {
		if position%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return File{}, err
			}
		}
		if rankAt(position).Sign() != 0 {
			return File{}, fmt.Errorf("invalid dense padding at position %d", position)
		}
	}
	stream := make([]byte, length)
	chunks := (length + denseChunkBytes - 1) / denseChunkBytes
	workers = min(workers, chunks)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	var failure error
	var failureOnce sync.Once
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			symbols := make([]*big.Int, fallback.geometry.digits[denseChunkBytes])
			digits := make([]uint16, fallback.geometry.digits[denseChunkBytes])
			for workCtx.Err() == nil {
				index := int(next.Add(1) - 1)
				if index >= chunks {
					return
				}
				start, bytes, _ := g.chunk(length, index)
				selected := plan[index]
				var conversionError error
				if selected.fallback {
					count := fallback.geometry.digits[bytes]
					for offset := range count {
						mapped := ordinaryDigits[v.Blocks[selected.position+offset]]
						if mapped < 0 {
							conversionError = fmt.Errorf("banner in fallback chunk")
							break
						}
						digits[offset] = uint16(mapped)
					}
					if conversionError == nil {
						conversionError = ordinaryDigitsToBytes(stream[start:start+bytes], digits[:count])
					}
				} else {
					count := g.digits[bytes]
					for offset := range count {
						symbols[offset] = rankAt(selected.position + offset)
					}
					conversionError = symbolsToBytes(stream[start:start+bytes], symbols[:count], g)
				}
				if err := conversionError; err != nil {
					failureOnce.Do(func() { failure = fmt.Errorf("dense chunk %d: %w", index, err); cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if failure != nil {
		return File{}, failure
	}
	if err := ctx.Err(); err != nil {
		return File{}, fmt.Errorf("decode: %w", err)
	}
	name := string(stream[:nameLength])
	if err := validateName(name); err != nil {
		return File{}, fmt.Errorf("decode filename: %w", err)
	}
	payloadStart := nameLength + manifestLength
	payload := stream[payloadStart:]
	var part *PartInfo
	if manifestLength != 0 {
		decoded := unmarshalDensePart(stream[nameLength:payloadStart])
		if err := validateDensePart(decoded, payloadLength); err != nil {
			return File{}, fmt.Errorf("decode multipart manifest: %w", err)
		}
		part = &decoded
	}
	if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(header[19:23]) {
		return File{}, fmt.Errorf("payload checksum mismatch")
	}
	if part != nil {
		if err := validateDensePartChecksum(*part, payload); err != nil {
			return File{}, err
		}
	}
	return File{Name: name, Payload: payload, Part: part}, nil
}
