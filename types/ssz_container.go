package types

import (
	"fmt"
	. "github.com/protolambda/zssz/dec"
	. "github.com/protolambda/zssz/enc"
	. "github.com/protolambda/zssz/htr"
	"github.com/protolambda/zssz/merkle"
	"github.com/protolambda/zssz/util/tags"
	"reflect"
	"unsafe"
)

const SSZ_TAG = "ssz"
const OMIT_FLAG = "omit"
const SQUASH_FLAG = "squash"

type FieldPtrFn func(p unsafe.Pointer) unsafe.Pointer

func (fn FieldPtrFn) WrapOffset(memOffset uintptr) FieldPtrFn {
	return func(p unsafe.Pointer) unsafe.Pointer {
		return fn(unsafe.Pointer(uintptr(p) + memOffset))
	}
}

type ContainerField struct {
	ssz   SSZ
	name  string
	ptrFn FieldPtrFn
}

func (c *ContainerField) Wrap(name string, memOffset uintptr) ContainerField {
	return ContainerField{
		ssz:   c.ssz,
		name:  name + ">" + c.name,
		ptrFn: c.ptrFn.WrapOffset(memOffset),
	}
}

type SquashableFields interface {
	// Get the ContainerFields
	SquashFields() []ContainerField
}

func GetOffsetPtrFn(memOffset uintptr) FieldPtrFn {
	return func(p unsafe.Pointer) unsafe.Pointer {
		return unsafe.Pointer(uintptr(p) + memOffset)
	}
}

type SSZContainer struct {
	Fields      []ContainerField
	isFixedLen  bool
	fixedLen    uint64
	minLen      uint64
	maxLen      uint64
	offsetCount uint64 // includes offsets for fields that are squashed in
	fuzzMinLen  uint64
	fuzzMaxLen  uint64
}

func (v *SSZContainer) SquashFields() []ContainerField {
	return v.Fields
}

// Get the container fields for the given struct field
// 0 fields (nil) if struct field is ignored
// 1 field for normal struct fields
// 0 or more fields when a struct field is squashed (recursively adding to the total field collection)
func getFields(factory SSZFactoryFn, f *reflect.StructField) (out []ContainerField, err error) {
	if tags.HasFlag(f, SSZ_TAG, OMIT_FLAG) {
		return nil, nil
	}
	fieldSSZ, err := factory(f.Type)
	if err != nil {
		return nil, err
	}

	forceSquash := tags.HasFlag(f, SSZ_TAG, SQUASH_FLAG)

	if f.Anonymous || forceSquash {
		if squashable, ok := fieldSSZ.(SquashableFields); ok {
			for _, sq := range squashable.SquashFields() {
				out = append(out, sq.Wrap(f.Name, f.Offset))
			}
			return out, nil
		}
		// anonymous fields can be handled as normal fields. Only error when it was tagged to be squashed.
		if forceSquash {
			return nil, fmt.Errorf("could not squash field %s", f.Name)
		}
	}

	out = append(out, ContainerField{ssz: fieldSSZ, name: f.Name, ptrFn: GetOffsetPtrFn(f.Offset)})
	return
}

func NewSSZContainer(factory SSZFactoryFn, typ reflect.Type) (*SSZContainer, error) {
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("typ is not a struct")
	}
	res := new(SSZContainer)
	for i, c := 0, typ.NumField(); i < c; i++ {
		// get the Go struct field
		sField := typ.Field(i)
		// For this field, get the SSZ field(s). There may be more if the Go field is squashed.
		fields, err := getFields(factory, &sField)
		if err != nil {
			return nil, err
		}
		res.Fields = append(res.Fields, fields...)
	}
	for _, field := range res.Fields {
		if field.ssz.IsFixed() {
			fixed, min, max := field.ssz.FixedLen(), field.ssz.MinLen(), field.ssz.MaxLen()
			if fixed != min || fixed != max {
				return nil, fmt.Errorf("fixed-size field ('%s') in struct has invalid min/max length", field.name)
			}
			res.fixedLen += fixed
			res.minLen += fixed
			res.maxLen += fixed
		} else {
			res.fixedLen += BYTES_PER_LENGTH_OFFSET
			res.minLen += BYTES_PER_LENGTH_OFFSET + field.ssz.MinLen()
			res.maxLen += BYTES_PER_LENGTH_OFFSET + field.ssz.MaxLen()
			res.offsetCount++
		}
		res.fuzzMinLen += field.ssz.FuzzMinLen()
		res.fuzzMaxLen += field.ssz.FuzzMaxLen()
	}
	res.isFixedLen = res.offsetCount == 0
	return res, nil
}

func (v *SSZContainer) FuzzMinLen() uint64 {
	return v.fuzzMinLen
}

func (v *SSZContainer) FuzzMaxLen() uint64 {
	return v.fuzzMaxLen
}

func (v *SSZContainer) MinLen() uint64 {
	return v.minLen
}

func (v *SSZContainer) MaxLen() uint64 {
	return v.maxLen
}

func (v *SSZContainer) FixedLen() uint64 {
	return v.fixedLen
}

func (v *SSZContainer) IsFixed() bool {
	return v.isFixedLen
}

func (v *SSZContainer) SizeOf(p unsafe.Pointer) uint64 {
	out := v.fixedLen
	for _, f := range v.Fields {
		if !f.ssz.IsFixed() {
			out += f.ssz.SizeOf(f.ptrFn(p))
		}
	}
	return out
}

func (v *SSZContainer) Encode(eb *EncodingBuffer, p unsafe.Pointer) {
	for _, f := range v.Fields {
		if f.ssz.IsFixed() {
			f.ssz.Encode(eb, f.ptrFn(p))
		} else {
			// write an offset to the fixed data, to find the dynamic data with as a reader
			eb.WriteOffset(v.fixedLen)

			// encode the dynamic data to a temporary buffer
			temp := GetPooledBuffer()
			f.ssz.Encode(temp, f.ptrFn(p))
			// write it forward
			eb.WriteForward(temp)

			ReleasePooledBuffer(temp)
		}
	}
	// Only flush if we need to.
	// If not, forward can actually be filled with data from the parent container, and should not be flushed.
	if !v.IsFixed() {
		// All the dynamic data is appended to the fixed data
		eb.FlushForward()
	}
}

func (v *SSZContainer) decodeVarSizeFuzzmode(dr *DecodingReader, p unsafe.Pointer) error {
	lengthLeftOver := v.fuzzMinLen

	for _, f := range v.Fields {
		lengthLeftOver -= f.ssz.FuzzMinLen()
		span := dr.GetBytesSpan()
		if span < lengthLeftOver {
			return fmt.Errorf("under estimated length requirements for fuzzing input, not enough data available to fuzz")
		}
		available := span - lengthLeftOver

		scoped, err := dr.Scope(available)
		if err != nil {
			return err
		}
		scoped.EnableFuzzMode()
		if err := f.ssz.Decode(scoped, f.ptrFn(p)); err != nil {
			return err
		}
		dr.UpdateIndexFromScoped(scoped)
	}
	return nil
}

func decodeOffsetElem(dr *DecodingReader, elemPtr unsafe.Pointer, decFn DecoderFn, expectedOffset uint64, scope uint64) error {
	currentOffset := dr.Index()
	if expectedOffset != currentOffset {
		return fmt.Errorf("expected to be at %d bytes, but currently at %d", expectedOffset, currentOffset)
	}
	scoped, err := dr.Scope(scope)
	if err != nil {
		return err
	}
	if err := decFn(scoped, elemPtr); err != nil {
		return err
	}
	dr.UpdateIndexFromScoped(scoped)
	return nil
}

func (v *SSZContainer) decodeDynamicPart(dr *DecodingReader, p unsafe.Pointer, offsets []uint64) error {
	i := 0
	for _, f := range v.Fields {
		// ignore fixed-size fields
		if f.ssz.IsFixed() {
			continue
		}
		// calculate the scope based on next offset, and max. value of this scope for the last value
		var scope uint64
		currentOffset := offsets[i]
		if next := i + 1; next < len(offsets) {
			if nextOffset := offsets[next]; nextOffset >= currentOffset {
				scope = nextOffset - currentOffset
			} else {
				return fmt.Errorf("offset %d for field %s is invalid", i, f.name)
			}
		} else {
			scope = dr.Max() - currentOffset
		}
		if err := decodeOffsetElem(dr, f.ptrFn(p), f.ssz.Decode, offsets[i], scope); err != nil {
			return err
		}
		// go to next offset
		i++
	}
	return nil
}

func (v *SSZContainer) decodeFixedPart(dr *DecodingReader, p unsafe.Pointer) ([]uint64, error) {
	// technically we could also ignore offset correctness and skip ahead,
	//  but we may want to enforce proper offsets.
	offsets := make([]uint64, 0, v.offsetCount)
	startIndex := dr.Index()
	fixedI := uint64(dr.Index())
	for _, f := range v.Fields {
		if f.ssz.IsFixed() {
			fixedI += f.ssz.FixedLen()
			// No need to redefine the scope for fixed-length SSZ objects.
			if err := f.ssz.Decode(dr, f.ptrFn(p)); err != nil {
				return nil, err
			}
		} else {
			fixedI += BYTES_PER_LENGTH_OFFSET
			// write an offset to the fixed data, to find the dynamic data with as a reader
			offset, err := dr.ReadOffset()
			if err != nil {
				return nil, err
			}
			offsets = append(offsets, offset)
		}
		if i := dr.Index(); i != fixedI {
			return nil, fmt.Errorf("fixed part had different size than expected, now at %d, expected to be at %d", i, fixedI)
		}
	}
	pivotIndex := dr.Index()
	if expectedIndex := v.fixedLen + startIndex; pivotIndex != expectedIndex {
		return nil, fmt.Errorf("expected to read to %d bytes for fixed part of container, got to %d", expectedIndex, pivotIndex)
	}
	return offsets, nil
}

func (v *SSZContainer) decodeVarSize(dr *DecodingReader, p unsafe.Pointer) error {
	offsets, err := v.decodeFixedPart(dr, p)
	if err != nil {
		return err
	}
	// not really squashed, but now that we have the offsets, we can decode it like this.
	return v.decodeDynamicPart(dr, p, offsets)
}

func (v *SSZContainer) Decode(dr *DecodingReader, p unsafe.Pointer) error {
	if dr.IsFuzzMode() {
		return v.decodeVarSizeFuzzmode(dr, p)
	} else {
		return v.decodeVarSize(dr, p)
	}
}

func (v *SSZContainer) HashTreeRoot(h HashFn, p unsafe.Pointer) [32]byte {
	leaf := func(i uint64) []byte {
		f := v.Fields[i]
		r := f.ssz.HashTreeRoot(h, f.ptrFn(p))
		return r[:]
	}
	leafCount := uint64(len(v.Fields))
	return merkle.Merkleize(h, leafCount, leafCount, leaf)
}

func (v *SSZContainer) SigningRoot(h HashFn, p unsafe.Pointer) [32]byte {
	leaf := func(i uint64) []byte {
		f := v.Fields[i]
		r := f.ssz.HashTreeRoot(h, f.ptrFn(p))
		return r[:]
	}
	// truncate last field
	leafCount := uint64(len(v.Fields))
	if leafCount != 0 {
		leafCount--
	}
	return merkle.Merkleize(h, leafCount, leafCount, leaf)
}
