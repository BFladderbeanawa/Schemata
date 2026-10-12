package schematic

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const maxNBT = 256 * 1024 * 1024
const maxNodes = 262144

type tag struct {
	kind     byte
	num      int64
	text     string
	raw      []byte
	list     []tag
	compound map[string]tag
	element  byte
}

type parser struct {
	data  []byte
	pos   int
	nodes int
}

func (p *parser) take(n int) ([]byte, error) {
	if n < 0 || n > len(p.data)-p.pos {
		return nil, fmt.Errorf("truncated NBT payload")
	}
	b := p.data[p.pos : p.pos+n]
	p.pos += n
	return b, nil
}

func (p *parser) number(n int) (int64, error) {
	b, err := p.take(n)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return int64(int8(b[0])), nil
	case 2:
		return int64(int16(binary.BigEndian.Uint16(b))), nil
	case 4:
		return int64(int32(binary.BigEndian.Uint32(b))), nil
	case 8:
		return int64(binary.BigEndian.Uint64(b)), nil
	}
	return 0, fmt.Errorf("invalid numeric width")
}

func (p *parser) string() (string, error) {
	b, err := p.take(2)
	if err != nil {
		return "", err
	}
	b, err = p.take(int(binary.BigEndian.Uint16(b)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (p *parser) payload(kind byte, depth int) (tag, error) {
	t := tag{kind: kind}
	p.nodes++
	if depth > 64 || p.nodes > maxNodes {
		return t, fmt.Errorf("NBT nesting or node limit exceeded")
	}
	var err error
	switch kind {
	case 1, 2, 3, 4, 5, 6:
		width := [...]int{0, 1, 2, 4, 8, 4, 8}[kind]
		t.num, err = p.number(width)
	case 8:
		t.text, err = p.string()
	case 7, 11, 12:
		var count int64
		count, err = p.number(4)
		if err != nil {
			break
		}
		width := 1
		if kind == 11 {
			width = 4
		}
		if kind == 12 {
			width = 8
		}
		if count < 0 || count > int64((len(p.data)-p.pos)/width) {
			return t, fmt.Errorf("invalid or truncated NBT array length %d", count)
		}
		t.raw, err = p.take(int(count) * width)
	case 9:
		var b []byte
		b, err = p.take(1)
		if err != nil {
			break
		}
		t.element = b[0]
		var count int64
		count, err = p.number(4)
		if err != nil {
			break
		}
		if count < 0 || count > int64(maxNodes-p.nodes) || count > int64(len(p.data)-p.pos) {
			return t, fmt.Errorf("invalid NBT list length %d", count)
		}
		if t.element > 12 || (t.element == 0 && count != 0) {
			return t, fmt.Errorf("invalid NBT list element type")
		}
		// Even variable-size elements have a minimum wire footprint. Check it
		// before allocating the list, as for all primitive array lengths.
		minimum := [...]int{1, 1, 2, 4, 8, 4, 8, 4, 2, 5, 1, 4, 4}
		if count > int64((len(p.data)-p.pos)/minimum[t.element]) {
			return t, fmt.Errorf("truncated NBT list payload")
		}
		t.list = make([]tag, int(count))
		for i := range t.list {
			t.list[i], err = p.payload(t.element, depth+1)
			if err != nil {
				break
			}
		}
	case 10:
		t.compound = make(map[string]tag)
		for {
			var b []byte
			b, err = p.take(1)
			if err != nil || b[0] == 0 {
				break
			}
			var name string
			name, err = p.string()
			if err != nil {
				break
			}
			if _, exists := t.compound[name]; exists {
				return t, fmt.Errorf("duplicate NBT field %q", name)
			}
			var child tag
			child, err = p.payload(b[0], depth+1)
			if err != nil {
				break
			}
			t.compound[name] = child
		}
	default:
		err = fmt.Errorf("invalid NBT tag type %d", kind)
	}
	return t, err
}

func parseNBT(data []byte) (string, tag, error) {
	p := parser{data: data}
	b, err := p.take(1)
	if err != nil {
		return "", tag{}, err
	}
	if b[0] != 10 {
		return "", tag{}, fmt.Errorf("NBT root must be a compound")
	}
	name, err := p.string()
	if err != nil {
		return "", tag{}, err
	}
	t, err := p.payload(10, 1)
	if err == nil && p.pos != len(data) {
		err = fmt.Errorf("trailing NBT data")
	}
	return name, t, err
}

// validateNBTOutput applies the parser's independent limits before gzip writes
// anything. Array elements count as bytes, not individual NBT nodes.
func validateNBTOutput(name string, root tag) error {
	b := nbtBudget{}
	if root.kind != 10 {
		return fmt.Errorf("NBT root must be a compound")
	}
	return b.named(name, root, 1)
}

type nbtBudget struct {
	bytes int
	nodes int
}

func (b *nbtBudget) add(n int) error {
	if n < 0 || n > maxNBT-b.bytes {
		return fmt.Errorf("serialized NBT exceeds 256 MiB limit")
	}
	b.bytes += n
	return nil
}

func (b *nbtBudget) string(s string) error {
	if len(s) > 65535 {
		return fmt.Errorf("NBT string too long")
	}
	return b.add(2 + len(s))
}

func (b *nbtBudget) named(name string, t tag, depth int) error {
	if err := b.add(1); err != nil {
		return err
	}
	if err := b.string(name); err != nil {
		return err
	}
	return b.payload(t, depth)
}

func (b *nbtBudget) payload(t tag, depth int) error {
	b.nodes++
	if depth > 64 || b.nodes > maxNodes {
		return fmt.Errorf("NBT nesting or node limit exceeded")
	}
	switch t.kind {
	case 1, 2, 3, 4, 5, 6:
		return b.add([...]int{0, 1, 2, 4, 8, 4, 8}[t.kind])
	case 8:
		return b.string(t.text)
	case 7, 11, 12:
		width := 1
		if t.kind == 11 {
			width = 4
		}
		if t.kind == 12 {
			width = 8
		}
		if len(t.raw)%width != 0 || len(t.raw)/width > 2147483647 {
			return fmt.Errorf("invalid NBT output array length")
		}
		return b.add(4 + len(t.raw))
	case 9:
		if t.element > 12 || (t.element == 0 && len(t.list) != 0) || len(t.list) > maxNodes-b.nodes {
			return fmt.Errorf("invalid NBT output list")
		}
		if err := b.add(5); err != nil {
			return err
		}
		for _, child := range t.list {
			if child.kind != t.element {
				return fmt.Errorf("inconsistent NBT output list element")
			}
			if err := b.payload(child, depth+1); err != nil {
				return err
			}
		}
	case 10:
		for key, child := range t.compound {
			if err := b.named(key, child, depth+1); err != nil {
				return err
			}
		}
		return b.add(1)
	default:
		return fmt.Errorf("invalid NBT output type")
	}
	return nil
}

type nbtWriter struct {
	w   io.Writer
	err error
}

func (w *nbtWriter) bytes(b []byte) {
	if w.err != nil {
		return
	}
	var n int
	n, w.err = w.w.Write(b)
	if w.err == nil && n != len(b) {
		w.err = io.ErrShortWrite
	}
}
func (w *nbtWriter) number(n int64, size int) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	w.bytes(b[8-size:])
}
func (w *nbtWriter) string(s string) {
	if len(s) > 65535 {
		w.err = fmt.Errorf("NBT string too long")
		return
	}
	w.number(int64(len(s)), 2)
	w.bytes([]byte(s))
}
func (w *nbtWriter) named(name string, t tag) {
	w.number(int64(t.kind), 1)
	w.string(name)
	w.payload(t)
}
func (w *nbtWriter) payload(t tag) {
	switch t.kind {
	case 1, 2, 3, 4, 5, 6:
		width := [...]int{0, 1, 2, 4, 8, 4, 8}[t.kind]
		w.number(t.num, width)
	case 8:
		w.string(t.text)
	case 7, 11, 12:
		width := 1
		if t.kind == 11 {
			width = 4
		}
		if t.kind == 12 {
			width = 8
		}
		w.number(int64(len(t.raw)/width), 4)
		w.bytes(t.raw)
	case 9:
		w.number(int64(t.element), 1)
		w.number(int64(len(t.list)), 4)
		for _, child := range t.list {
			w.payload(child)
		}
	case 10:
		keys := make([]string, 0, len(t.compound))
		for key := range t.compound {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			w.named(key, t.compound[key])
		}
		w.number(0, 1)
	default:
		w.err = fmt.Errorf("invalid NBT output type")
	}
}

func compound(fields map[string]tag) tag { return tag{kind: 10, compound: fields} }
func integer(n int) tag                  { return tag{kind: 3, num: int64(n)} }
func text(s string) tag                  { return tag{kind: 8, text: s} }
func vector(x, y, z int) tag {
	return compound(map[string]tag{"x": integer(x), "y": integer(y), "z": integer(z)})
}
func emptyList() tag { return tag{kind: 9, element: 10, list: []tag{}} }
func field(t tag, name string, kind byte) (tag, error) {
	v, ok := t.compound[name]
	if !ok || v.kind != kind {
		return tag{}, fmt.Errorf("missing or invalid %q NBT field", name)
	}
	return v, nil
}
