package ssz

import (
	"fmt"
	"reflect"
	"unsafe"
	"zrnt-ssz/ssz/endianness"
	"zrnt-ssz/ssz/unsafe_util"
)

type SSZBasicList struct {
	elemKind reflect.Kind
	elemSSZ *SSZBasic
}

func NewSSZBasicList(typ reflect.Type) (*SSZBasicList, error) {
	if typ.Kind() != reflect.Slice {
		return nil, fmt.Errorf("typ is not a dynamic-length array")
	}
	elemTyp := typ.Elem()
	elemKind := elemTyp.Kind()
	elemSSZ, err := GetBasicSSZElemType(elemKind)
	if err != nil {
		return nil, err
	}
	if elemSSZ.Length != uint32(elemTyp.Size()) {
		return nil, fmt.Errorf("basic element type has different size than SSZ type unexpectedly, ssz: %d, go: %d", elemSSZ.Length, elemTyp.Size())
	}

	res := &SSZBasicList{
		elemKind: elemKind,
		elemSSZ: elemSSZ,
	}
	return res, nil
}

func (v *SSZBasicList) FixedLen() uint32 {
	return 0
}

func (v *SSZBasicList) IsFixed() bool {
	return false
}

func (v *SSZBasicList) Encode(eb *sszEncBuf, p unsafe.Pointer) {
	sh := unsafe_util.ReadSliceHeader(p)

	// we can just write the data as-is in a few contexts:
	// - if we're in a little endian architecture
	// - if there is no endianness to deal with
	if endianness.IsLittleEndian || v.elemSSZ.Length == 1 {
		bytesSh := unsafe_util.GetSliceHeader(unsafe.Pointer(sh.Data), uint32(sh.Len) * v.elemSSZ.Length)
		data := *(*[]byte)(unsafe.Pointer(bytesSh))
		eb.Write(data)
	} else {
		EncodeFixedSeries(v.elemSSZ.Encoder, uint32(sh.Len), uintptr(v.elemSSZ.Length), eb, unsafe.Pointer(sh.Data))
	}
}

func (v *SSZBasicList) Decode(dr *SSZDecReader, p unsafe.Pointer) error {
	bytesLen := dr.Max() - dr.Index()
	if bytesLen % v.elemSSZ.Length != 0 {
		return fmt.Errorf("cannot decode basic type array, input has is")
	}
	elemMemSize := uintptr(v.elemSSZ.Length)
	contentsPtr := unsafe_util.AllocateSliceSpaceAndBind(p, bytesLen / v.elemSSZ.Length, elemMemSize)

	if endianness.IsLittleEndian || v.elemSSZ.Length == 1 {
		bytesSh := unsafe_util.GetSliceHeader(contentsPtr, bytesLen)
		data := *(*[]byte)(unsafe.Pointer(bytesSh))
		if _, err := dr.Read(data); err != nil {
			return err
		}
		if v.elemKind == reflect.Bool {
			for i := 0; i < len(data); i++ {
				if data[i] > 1 {
					return fmt.Errorf("byte %d in bool list is not a valid bool value: %d", i, data[i])
				}
			}
		}
		return nil
	} else {
		return DecodeFixedSlice(v.elemSSZ.Decoder, v.elemSSZ.FixedLen(), bytesLen, elemMemSize, dr, p)
	}
}

func (v *SSZBasicList) HashTreeRoot(h *Hasher, p unsafe.Pointer) [32]byte {
	//elemSize := v.elemMemSize
	sh := unsafe_util.ReadSliceHeader(p)

	if endianness.IsLittleEndian || v.elemSSZ.Length == 1 {
		bytesSh := unsafe_util.GetSliceHeader(unsafe.Pointer(sh.Data), uint32(sh.Len) * v.elemSSZ.Length)
		data := *(*[]byte)(unsafe.Pointer(bytesSh))
		dataLen := uint32(len(data))

		pow := v.elemSSZ.ChunkPow
		leaf := func(i uint32) []byte {
			s := i << pow
			e := (i + 1) << pow
			// pad the data
			if e > dataLen {
				d := [32]byte{}
				copy(d[:], data[s:dataLen])
				return d[:]
			}
			return data[s:e]
		}
		return Merkleize(h, uint32(sh.Len), leaf)
	} else {
		bytesSh := unsafe_util.GetSliceHeader(unsafe.Pointer(sh.Data), uint32(sh.Len) * v.elemSSZ.Length)
		data := *(*[]byte)(unsafe.Pointer(bytesSh))
		dataLen := uint32(len(data))
		pow := v.elemSSZ.ChunkPow
		leaf := func(i uint32) []byte {
			s := i << pow
			e := (i + 1) << pow
			d := [32]byte{}
			x := 31
			for j := s; j < e && j < dataLen; j++ {
				d[x] = data[x]
				x--
			}
			return d[:]
		}
		return Merkleize(h, uint32(sh.Len), leaf)
	}
}
