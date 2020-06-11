package dec

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"unsafe"
)

type DecoderFn func(dr *DecodingReader, pointer unsafe.Pointer) error
type DryCheckFn func(dr *DecodingReader) error

type DecodingReader struct {
	input    io.Reader
	i        uint64
	max      uint64
	fuzzMode bool
	scratch  [32]byte
}

func NewDecodingReader(input io.Reader) *DecodingReader {
	return &DecodingReader{input: input, i: 0, max: ^uint64(0)}
}

// returns a scope of the SSZ reader. Re-uses same scratchpad.
func (dr *DecodingReader) Scope(count uint64) (*DecodingReader, error) {
	if span := dr.GetBytesSpan(); span < count {
		return nil, fmt.Errorf("cannot create scoped decoding reader, scope of %d bytes is bigger than parent scope has available space %d", count, span)
	}
	return &DecodingReader{input: io.LimitReader(dr.input, int64(count)), i: 0, max: count}, nil
}

func (dr *DecodingReader) EnableFuzzMode() {
	dr.fuzzMode = true
}

func (dr *DecodingReader) UpdateIndexFromScoped(other *DecodingReader) {
	dr.i += other.i
}

// how far we have read so far (scoped per container)
func (dr *DecodingReader) Index() uint64 {
	return dr.i
}

// How far we can read (max - i = remaining bytes to read without error).
// Note: when a child element is not fixed length,
// the parent should set the scope, so that the child can infer its size from it.
func (dr *DecodingReader) Max() uint64 {
	return dr.max
}

func (dr *DecodingReader) checkedIndexUpdate(x uint64) (n int, err error) {
	v := dr.i + x
	if v > dr.max {
		return int(dr.i), fmt.Errorf("cannot read %d bytes, %d beyond scope", x, v-dr.max)
	}
	dr.i = v
	return int(x), nil
}

func (dr *DecodingReader) Skip(count uint64) (int, error) {
	if n, err := dr.checkedIndexUpdate(count); err != nil {
		return n, err
	}
	switch r := dr.input.(type) {
	case io.Seeker:
		n, err := r.Seek(int64(count), io.SeekCurrent)
		return int(n), err
	default:
		n, err := io.CopyN(ioutil.Discard, dr.input, int64(count))
		return int(n), err
	}
}

func (dr *DecodingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if n, err := dr.checkedIndexUpdate(uint64(len(p))); err != nil {
		return n, err
	}
	n := 0
	for n < len(p) {
		v, err := dr.input.Read(p[n:])
		n += v
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (dr *DecodingReader) ReadByte() (byte, error) {
	_, err := dr.Read(dr.scratch[0:1])
	return dr.scratch[0], err
}

func (dr *DecodingReader) ReadUint16() (uint16, error) {
	_, err := dr.Read(dr.scratch[0:2])
	return binary.LittleEndian.Uint16(dr.scratch[0:2]), err
}

func (dr *DecodingReader) ReadUint32() (uint32, error) {
	_, err := dr.Read(dr.scratch[0:4])
	return binary.LittleEndian.Uint32(dr.scratch[0:4]), err
}

func (dr *DecodingReader) ReadUint64() (uint64, error) {
	_, err := dr.Read(dr.scratch[0:8])
	return binary.LittleEndian.Uint64(dr.scratch[0:8]), err
}

// returns the remaining span that can be read
func (dr *DecodingReader) GetBytesSpan() uint64 {
	return dr.Max() - dr.Index()
}

// Reads an offset, and wraps it in maximum size, a uint64, for safety.
func (dr *DecodingReader) ReadOffset() (uint64, error) {
	v, err := dr.ReadUint32()
	return uint64(v), err
}

// if normal, offsets are used and enforced.
// if fuzzMode, no offsets are used, and lengths are read from the input, and adjusted to match remaining space.
func (dr *DecodingReader) IsFuzzMode() bool {
	return dr.fuzzMode
}
