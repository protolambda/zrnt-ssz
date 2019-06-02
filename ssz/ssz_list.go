package ssz

import (
	"fmt"
	"reflect"
	"unsafe"
	"zrnt-ssz/ssz/unsafe_util"
)

type SSZList struct {
	elemMemSize uintptr
	elemSSZ SSZ
}

func NewSSZList(factory SSZFactoryFn, typ reflect.Type) (*SSZList, error) {
	if typ.Kind() != reflect.Slice {
		return nil, fmt.Errorf("typ is not a dynamic-length array")
	}
	elemTyp := typ.Elem()

	elemSSZ, err := factory(elemTyp)
	if err != nil {
		return nil, err
	}
	res := &SSZList{
		elemMemSize: elemTyp.Size(),
		elemSSZ: elemSSZ,
	}
	return res, nil
}

func (v *SSZList) FixedLen() uint32 {
	return 0
}

func (v *SSZList) IsFixed() bool {
	return false
}

func (v *SSZList) Encode(eb *sszEncBuf, p unsafe.Pointer) {
	sh := unsafe_util.ReadSliceHeader(p)
	if v.elemSSZ.IsFixed() {
		EncodeFixedSeries(v.elemSSZ.Encode, uint32(sh.Len), v.elemMemSize, eb, unsafe.Pointer(sh.Data))
	} else {
		EncodeVarSeries(v.elemSSZ.Encode, uint32(sh.Len), v.elemMemSize, eb, unsafe.Pointer(sh.Data))
	}
}

func (v *SSZList) Decode(dr *SSZDecReader, p unsafe.Pointer) error {
	bytesLen := dr.Max() - dr.Index()
	if v.elemSSZ.IsFixed() {
		return DecodeFixedSlice(v.elemSSZ.Decode, v.elemSSZ.FixedLen(), bytesLen, v.elemMemSize, dr, p)
	} else {
		// still pass the fixed length of the element, but just to check a minimum length requirement.
		return DecodeVarSlice(v.elemSSZ.Decode, v.elemSSZ.FixedLen(), bytesLen, v.elemMemSize, dr, p)
	}
}

func (v *SSZList) HashTreeRoot(h *Hasher, p unsafe.Pointer) []byte {
	elemHtr := v.elemSSZ.HashTreeRoot
	elemSize := v.elemMemSize
	sh := unsafe_util.ReadSliceHeader(p)
	leaf := func(i uint32) []byte {
		return elemHtr(h, unsafe.Pointer(sh.Data+(elemSize * uintptr(i))))
	}
	return Merkleize(h, uint32(sh.Len), leaf)
}
