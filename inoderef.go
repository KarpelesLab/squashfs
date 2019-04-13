package squashfs

import "fmt"

type inodeRef uint64

func (i inodeRef) Index() uint32 {
	return uint32((uint64(i) >> 16) & 0xffffffff)
}

func (i inodeRef) Offset() uint32 {
	return uint32(uint64(i) & 0xffff)
}

func (i inodeRef) String() string {
	return fmt.Sprintf("inodeRef(index=0x%x,offset=0x%x)", i.Index(), i.Offset())
}
