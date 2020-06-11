package ptrutil

import (
	"reflect"
	"runtime"
	"unsafe"
)

type SliceAllocationFn func(p unsafe.Pointer, length uint64) unsafe.Pointer

// Checks the capacity of the slice first. If sufficient, it mutates the length and returns the pointer to the contents.
// If not sufficient, it allocates a new slice using the underlying allocation function,
// and returns the pointer to the new contents.
func (f SliceAllocationFn) MutateLenOrAllocNew(p unsafe.Pointer, length uint64) unsafe.Pointer {
	header := ReadSliceHeader(p)
	if uint64(header.Cap) < length {
		// We don't want elements to be put in the slice header memory,
		// instead, we allocate the slice data with the allocation function,
		// and change the contents-pointer in the header.
		return f(p, length)
	} else {
		header.Len = int(length)
		return header.Data
	}
}

type AllocationFn func(p unsafe.Pointer) unsafe.Pointer

var bytesSliceTyp = reflect.TypeOf(new([]byte)).Elem()

func BytesAllocFn(p unsafe.Pointer, length uint64) unsafe.Pointer {
	return AllocateSliceSpaceAndBind(p, length, bytesSliceTyp)
}

func MakeSliceAllocFn(typ reflect.Type) SliceAllocationFn {
	return func(p unsafe.Pointer, length uint64) unsafe.Pointer {
		return AllocateSliceSpaceAndBind(p, length, typ)
	}
}

// Allocates a new slice of the given length, of the given type.
// Note: p is assumed to be a pointer to a slice header,
// and the pointer is assumed to keep the referenced data alive as long as necessary, away from the GC.
// The allocated space is zeroed out.
func AllocateSliceSpaceAndBind(p unsafe.Pointer, length uint64, typ reflect.Type) unsafe.Pointer {
	if length == 0 {
		pSh := (*SliceHeader)(p)
		pSh.Len = 0
		pSh.Cap = 0
		pSh.Data = unsafe.Pointer(nil)
		return pSh.Data
	}
	// for arrays/slices we need unsafe_New,
	// and resort to using reflect.MakeSlice to allocate the space, to be safe from the GC.
	l := int(length)
	newData := reflect.MakeSlice(typ, l, l)
	contentsPtr := unsafe.Pointer(newData.Pointer())
	pSh := (*SliceHeader)(p)
	pSh.Len = 0
	pSh.Data = contentsPtr
	pSh.Cap = l
	pSh.Len = l
	runtime.KeepAlive(&newData)
	return contentsPtr
}

// Allocates space of the given length and returns a pointer to the contents
// The allocated space is zeroed out.
func AllocateSpace(p unsafe.Pointer, typ reflect.Type) unsafe.Pointer {
	v := reflect.New(typ)
	ptr := unsafe.Pointer(v.Pointer())
	*(*unsafe.Pointer)(p) = ptr
	runtime.KeepAlive(&v)
	return ptr
}
