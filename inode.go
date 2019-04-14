package squashfs

import (
	"encoding/binary"
	"log"
	"os"

	"github.com/tardigradeos/tpkg/tpkgfs"
)

type Inode struct {
	sb *Superblock

	Type    uint16
	Perm    uint16
	UidIdx  uint16
	GidIdx  uint16
	ModTime int32
	Ino     uint32 // inode number
}

func (sb *Superblock) GetInode(ino uint64) (tpkgfs.Inode, error) {
	if ino == 1 {
		// get root inode
	}
	log.Printf("get inode WIP %d", ino)
	return nil, os.ErrInvalid
}

func (sb *Superblock) GetInodeRef(inor inodeRef) (tpkgfs.Inode, error) {
	r, err := sb.newInodeReader(inor)
	if err != nil {
		return nil, err
	}

	ino := &Inode{sb: sb}

	// read inode info
	err = binary.Read(r, sb.order, &ino.Type)
	if err != nil {
		return nil, err
	}
	err = binary.Read(r, sb.order, &ino.Perm)
	if err != nil {
		return nil, err
	}
	err = binary.Read(r, sb.order, &ino.UidIdx)
	if err != nil {
		return nil, err
	}
	err = binary.Read(r, sb.order, &ino.GidIdx)
	if err != nil {
		return nil, err
	}
	err = binary.Read(r, sb.order, &ino.ModTime)
	if err != nil {
		return nil, err
	}
	err = binary.Read(r, sb.order, &ino.Ino)
	if err != nil {
		return nil, err
	}

	log.Printf("read inode #%d type=%d", ino.Ino, ino.Type)

	return nil, os.ErrInvalid // TODO
}
