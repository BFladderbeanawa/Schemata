package codec

import (
	"context"
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
	denseVersion    = 3
	denseBootstrap  = prefixBytes * 2
	denseChunkBytes = 256
	MaxDensePayload = 32 * 1024 * 1024
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

// EncodeDense writes version 3 chunks with resource-aware nonbanner fallback.
// Only .litematic can serialize the returned arbitrary-state volume.
func EncodeDense(payload []byte, name string, opts layout.Options) (volume.Volume, error) {
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
	length := len(name) + len(payload)
	plan, required, err := planDenseChunks(payload, name, g, fallback)
	if err != nil {
		return volume.Volume{}, err
	}
	v, err := layout.SelectDense(required, opts)
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
	header[8] = denseVersion
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
	// Batches bound both conversion temporaries and completed results. Integrate
	// symbols in logical order so palette and banner output never depend on timing.
	for first := 0; first < chunks; first += workers {
		batch := min(workers, chunks-first)
		results := make([][]*big.Int, batch)
		var wg sync.WaitGroup
		wg.Add(batch)
		for worker := range batch {
			go func() {
				defer wg.Done()
				start, bytes, _ := g.chunk(length, first+worker)
				var chunk [denseChunkBytes]byte
				for offset := range bytes {
					logical := start + offset
					if logical < len(name) {
						chunk[offset] = name[logical]
					} else {
						chunk[offset] = payload[logical-len(name)]
					}
				}
				geometry := g
				if plan[first+worker].fallback {
					geometry = fallback.geometry
				}
				results[worker] = bytesToSymbols(chunk[:bytes], geometry)
			}()
		}
		wg.Wait()
		for worker, digits := range results {
			selected := plan[first+worker]
			marker := int64(0)
			if selected.fallback {
				marker = 1
			}
			if err := put(selected.position, big.NewInt(marker)); err != nil {
				return volume.Volume{}, err
			}
			for offset, rank := range digits {
				if selected.fallback {
					rank = big.NewInt(int64(fallback.ranks[rank.Int64()]))
				}
				if err := put(selected.position+1+offset, rank); err != nil {
					return volume.Volume{}, err
				}
			}
		}
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
	if header[8] != 2 && header[8] != denseVersion {
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
	length := nameLength + int(payloadLength)
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
	fallbackRanks := make([]*big.Int, len(fallback.ranks))
	for i := range fallbackRanks {
		fallbackRanks[i] = big.NewInt(int64(i))
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
			for workCtx.Err() == nil {
				index := int(next.Add(1) - 1)
				if index >= chunks {
					return
				}
				start, bytes, _ := g.chunk(length, index)
				selected := plan[index]
				geometry := g
				if selected.fallback {
					geometry = fallback.geometry
				}
				var conversionError error
				for offset := range geometry.digits[bytes] {
					rank := rankAt(selected.position + offset)
					if selected.fallback {
						if !rank.IsInt64() || rank.Int64() >= int64(len(fallback.indices)) {
							conversionError = fmt.Errorf("invalid fallback symbol")
							break
						}
						mapped := fallback.indices[rank.Int64()]
						if mapped < 0 {
							conversionError = fmt.Errorf("banner in fallback chunk")
							break
						}
						rank = fallbackRanks[mapped]
					}
					symbols[offset] = rank
				}
				if conversionError == nil {
					conversionError = symbolsToBytes(stream[start:start+bytes], symbols[:geometry.digits[bytes]], geometry)
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
	payload := stream[nameLength:]
	if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(header[19:23]) {
		return File{}, fmt.Errorf("payload checksum mismatch")
	}
	return File{Name: name, Payload: payload}, nil
}
