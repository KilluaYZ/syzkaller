// Copyright 2018 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package prog

import (
	"fmt"
)

type anyTypes struct {
	union  *UnionType
	array  *ArrayType
	blob   *BufferType
	ptrPtr *PtrType
	ptr64  *PtrType
	res8   *ResourceType
	res16  *ResourceType
	res32  *ResourceType
	res64  *ResourceType
	resdec *ResourceType
	reshex *ResourceType
	resoct *ResourceType
}

func (target *Target) initAnyTypes() {
	var anyPtrs *UnionType
	for _, typ := range target.Types {
		if typ.Name() == "ANYPTRS" {
			anyPtrs = typ.(*UnionType)
			break
		}
	}
	if anyPtrs == nil {
		panic("no builtin ANYPTRS type")
	}
	// These types are generated by builtin descriptions in pkg/compiler/types.go.
	target.any.ptrPtr = anyPtrs.Fields[0].Type.(*PtrType)
	target.any.ptr64 = anyPtrs.Fields[1].Type.(*PtrType)
	target.any.array = target.any.ptrPtr.Elem.(*ArrayType)
	target.any.union = target.any.array.Elem.(*UnionType)
	target.any.blob = target.any.union.Fields[0].Type.(*BufferType)
	target.any.res8 = target.any.union.Fields[1].Type.(*ResourceType)
	target.any.res16 = target.any.union.Fields[2].Type.(*ResourceType)
	target.any.res32 = target.any.union.Fields[3].Type.(*ResourceType)
	target.any.res64 = target.any.union.Fields[4].Type.(*ResourceType)
	target.any.resdec = target.any.union.Fields[5].Type.(*ResourceType)
	target.any.reshex = target.any.union.Fields[6].Type.(*ResourceType)
	target.any.resoct = target.any.union.Fields[7].Type.(*ResourceType)
}

func (target *Target) getAnyPtrType(size uint64) *PtrType {
	switch size {
	case target.PtrSize:
		return target.any.ptrPtr
	case 8:
		return target.any.ptr64
	}
	panic(fmt.Sprintf("bad pointer size %v", size))
}

func (target *Target) isAnyPtr(typ Type) bool {
	ptr, ok := typ.(*PtrType)
	return ok && ptr.Elem == target.any.array
}

type complexPtr struct {
	arg  *PointerArg
	call *Call
}

func (p *Prog) complexPtrs() (res []complexPtr) {
	for _, c := range p.Calls {
		ForeachArg(c, func(arg Arg, ctx *ArgCtx) {
			if ptrArg, ok := arg.(*PointerArg); ok && p.Target.isComplexPtr(ptrArg) {
				res = append(res, complexPtr{ptrArg, c})
				ctx.Stop = true
			}
		})
	}
	return
}

func (target *Target) isComplexPtr(arg *PointerArg) bool {
	if arg.Res == nil || !arg.Type().(*PtrType).SquashableElem {
		return false
	}
	if target.isAnyPtr(arg.Type()) {
		return true
	}
	complex := false
	ForeachSubArg(arg.Res, func(a1 Arg, ctx *ArgCtx) {
		switch typ := a1.Type().(type) {
		case *StructType:
			if typ.Varlen() {
				complex = true
				ctx.Stop = true
			}
		case *UnionType:
			if typ.Varlen() && len(typ.Fields) > 5 {
				complex = true
				ctx.Stop = true
			}
		}
	})
	return complex
}

func (target *Target) isAnyRes(name string) bool {
	return name == target.any.res8.TypeName ||
		name == target.any.res16.TypeName ||
		name == target.any.res32.TypeName ||
		name == target.any.res64.TypeName ||
		name == target.any.resdec.TypeName ||
		name == target.any.reshex.TypeName ||
		name == target.any.resoct.TypeName
}

func (target *Target) CallContainsAny(c *Call) (res bool) {
	ForeachArg(c, func(arg Arg, ctx *ArgCtx) {
		if target.isAnyPtr(arg.Type()) || res {
			res = true
			ctx.Stop = true
		}
	})
	return
}

func (target *Target) ArgContainsAny(arg0 Arg) (res bool) {
	ForeachSubArg(arg0, func(arg Arg, ctx *ArgCtx) {
		if target.isAnyPtr(arg.Type()) || res {
			res = true
			ctx.Stop = true
		}
	})
	return
}

func (target *Target) squashPtr(arg *PointerArg) {
	if arg.Res == nil || arg.VmaSize != 0 {
		panic("bad ptr arg")
	}
	res0 := arg.Res
	size0 := res0.Size()
	var elems []Arg
	target.squashPtrImpl(arg.Res, &elems)
	newType := target.getAnyPtrType(arg.Type().Size())
	arg.ref = newType.ref()
	arg.Res = MakeGroupArg(newType.Elem, DirIn, elems)
	if size := arg.Res.Size(); size != size0 {
		panic(fmt.Sprintf("squash changed size %v->%v for %v", size0, size, res0.Type()))
	}
}

func (target *Target) squashPtrImpl(a Arg, elems *[]Arg) {
	if a.Type().BitfieldLength() != 0 {
		panic("bitfield in squash")
	}
	var pad uint64
	switch arg := a.(type) {
	case *ConstArg:
		target.squashConst(arg, elems)
	case *ResultArg:
		target.squashResult(arg, elems)
	case *UnionArg:
		if !arg.Type().Varlen() {
			pad = arg.Size() - arg.Option.Size()
		}
		target.squashPtrImpl(arg.Option, elems)
	case *DataArg:
		if arg.Dir() == DirOut {
			pad = arg.Size()
		} else {
			elem := target.ensureDataElem(elems)
			elem.data = append(elem.Data(), arg.Data()...)
		}
	case *GroupArg:
		target.squashGroup(arg, elems)
	default:
		panic(fmt.Sprintf("bad arg kind %v (%#v) %v", a, a, a.Type()))
	}
	if pad != 0 {
		elem := target.ensureDataElem(elems)
		elem.data = append(elem.Data(), make([]byte, pad)...)
	}
}

func (target *Target) squashConst(arg *ConstArg, elems *[]Arg) {
	if IsPad(arg.Type()) {
		elem := target.ensureDataElem(elems)
		elem.data = append(elem.Data(), make([]byte, arg.Size())...)
		return
	}
	v, bf := target.squashedValue(arg)
	var data []byte
	switch bf {
	case FormatNative:
		for i := uint64(0); i < arg.Size(); i++ {
			data = append(data, byte(v))
			v >>= 8
		}
	case FormatStrDec:
		data = []byte(fmt.Sprintf("%020v", v))
	case FormatStrHex:
		data = []byte(fmt.Sprintf("0x%016x", v))
	case FormatStrOct:
		data = []byte(fmt.Sprintf("%023o", v))
	default:
		panic(fmt.Sprintf("unknown binary format: %v", bf))
	}
	if uint64(len(data)) != arg.Size() {
		panic("squashed value of wrong size")
	}
	elem := target.ensureDataElem(elems)
	elem.data = append(elem.Data(), data...)
}

func (target *Target) squashResult(arg *ResultArg, elems *[]Arg) {
	var typ *ResourceType
	index := -1
	switch arg.Type().Format() {
	case FormatNative, FormatBigEndian:
		switch arg.Size() {
		case 1:
			typ, index = target.any.res8, 1
		case 2:
			typ, index = target.any.res16, 2
		case 4:
			typ, index = target.any.res32, 3
		case 8:
			typ, index = target.any.res64, 4
		default:
			panic(fmt.Sprintf("bad size %v", arg.Size()))
		}
	case FormatStrDec:
		typ, index = target.any.resdec, 5
	case FormatStrHex:
		typ, index = target.any.reshex, 6
	case FormatStrOct:
		typ, index = target.any.resoct, 7
	default:
		panic("bad")
	}
	arg.ref = typ.ref()
	*elems = append(*elems, MakeUnionArg(target.any.union, DirIn, arg, index))
}

func (target *Target) squashGroup(arg *GroupArg, elems *[]Arg) {
	if typ, ok := arg.Type().(*StructType); ok && typ.OverlayField != 0 {
		panic("squashing out_overlay")
	}
	var bitfield, fieldsSize uint64
	for _, fld := range arg.Inner {
		fieldsSize += fld.Size()
		// Squash bitfields separately.
		if fld.Type().IsBitfield() {
			bfLen := fld.Type().BitfieldLength()
			bfOff := fld.Type().BitfieldOffset()
			// Note: we can have a ResultArg here as well,
			// but it is unsupported at the moment.
			v, bf := target.squashedValue(fld.(*ConstArg))
			if bf != FormatNative {
				panic(fmt.Sprintf("bitfield has bad format %v", bf))
			}
			bitfield |= (v & ((1 << bfLen) - 1)) << bfOff
			if fld.Size() != 0 {
				elem := target.ensureDataElem(elems)
				for i := uint64(0); i < fld.Size(); i++ {
					elem.data = append(elem.Data(), byte(bitfield))
					bitfield >>= 8
				}
				bitfield = 0
			}
			continue
		}
		target.squashPtrImpl(fld, elems)
	}
	// Add padding either due to dynamic alignment or overlay fields.
	if pad := arg.Size() - fieldsSize; pad != 0 {
		elem := target.ensureDataElem(elems)
		elem.data = append(elem.Data(), make([]byte, pad)...)
	}
}

func (target *Target) squashedValue(arg *ConstArg) (uint64, BinaryFormat) {
	typ := arg.Type()
	bf := typ.Format()
	if _, ok := typ.(*CsumType); ok {
		// We can't compute value for the checksum here,
		// but at least leave something recognizable by hints code.
		// TODO: hints code won't recognize this, because it won't find
		// the const in any arg. We either need to put this const as
		// actual csum arg value, or special case it in hints.
		return 0xabcdef1234567890, FormatNative
	}
	// Note: we need a constant value, but it depends on pid for proc.
	v, _ := arg.Value()
	if bf == FormatBigEndian {
		bf = FormatNative
		switch typ.UnitSize() {
		case 2:
			v = uint64(swap16(uint16(v)))
		case 4:
			v = uint64(swap32(uint32(v)))
		case 8:
			v = swap64(v)
		default:
			panic(fmt.Sprintf("bad const size %v", arg.Size()))
		}
	}
	return v, bf
}

func (target *Target) ensureDataElem(elems *[]Arg) *DataArg {
	if len(*elems) == 0 {
		res := MakeDataArg(target.any.blob, DirIn, nil)
		*elems = append(*elems, MakeUnionArg(target.any.union, DirIn, res, 0))
		return res
	}
	res, ok := (*elems)[len(*elems)-1].(*UnionArg).Option.(*DataArg)
	if !ok {
		res = MakeDataArg(target.any.blob, DirIn, nil)
		*elems = append(*elems, MakeUnionArg(target.any.union, DirIn, res, 0))
	}
	return res
}
