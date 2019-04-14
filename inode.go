package squashfs

import (
	"encoding/binary"
	"log"
	"os"

	"github.com/hanwen/go-fuse/fuse"
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

	StartBlock uint64
	NLink      uint32
	Size       uint64 // Careful, actual on disk size varies depending on type
	Offset     uint32 // uint16 for directories
	ParentIno  uint32 // for directories
}

func (sb *Superblock) GetInode(ino uint64) (tpkgfs.Inode, error) {
	if ino == 1 {
		// get root inode
		return sb.rootIno, nil
	}
	if ino == sb.rootInoN {
		// we reverse
		ino = 1
	}
	log.Printf("get inode WIP %d", ino)
	return nil, os.ErrInvalid
}

func (sb *Superblock) GetInodeRef(inor inodeRef) (*Inode, error) {
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

	switch ino.Type {
	case 1: // Basic Directory
		var u32 uint32
		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}
		ino.StartBlock = uint64(u32)

		err = binary.Read(r, sb.order, &ino.NLink)
		if err != nil {
			return nil, err
		}

		var u16 uint16
		err = binary.Read(r, sb.order, &u16)
		if err != nil {
			return nil, err
		}
		ino.Size = uint64(u16)

		err = binary.Read(r, sb.order, &u16)
		if err != nil {
			return nil, err
		}
		ino.Offset = uint32(u16)

		err = binary.Read(r, sb.order, &ino.ParentIno)
		if err != nil {
			return nil, err
		}

		log.Printf("squashfs: read basic directory success, parent=%d", ino.ParentIno)
	default:
		log.Printf("squashfs: unsupported inode type %d", ino.Type)
		return nil, os.ErrInvalid
	}

	return ino, nil
}

func (i *Inode) Lookup(name string) (uint64, error) {
	log.Printf("squashfs: lookup name %s from inode %d TODO", name, i.Ino)
	return 0, os.ErrInvalid
}

func (i *Inode) Mode() os.FileMode {
	res := os.FileMode(i.Perm)
	switch i.Type {
	case 1, 8: // Dir
		res |= os.ModeDir
	case 2, 9: // file
		// nothing
	case 3, 10:
		res |= os.ModeSymlink
	case 4, 11:
		res |= os.ModeDevice
	case 5, 12:
		res |= os.ModeCharDevice
	case 6, 13:
		res |= os.ModeNamedPipe
	case 7, 14:
		res |= os.ModeSocket
	}

	return res
}

func (i *Inode) IsDir() bool {
	switch i.Type {
	case 1, 8:
		return true
	}
	return false
}

func (i *Inode) FillAttr(attr *fuse.Attr) error {
	attr.Size = i.Size
	attr.Blocks = 1
	attr.Mode = tpkgfs.ModeToUnix(i.Mode())
	attr.Nlink = i.NLink // 1 required
	attr.Rdev = 1
	attr.Blksize = i.sb.BlockSize
	attr.Atime = uint64(i.ModTime)
	attr.Mtime = uint64(i.ModTime)
	attr.Ctime = uint64(i.ModTime)
	return nil
}

func (i *Inode) Readlink() ([]byte, error) {
	return nil, os.ErrInvalid
}

func (i *Inode) Open(flags uint32) error {
	return os.ErrInvalid
}

func (i *Inode) OpenDir() error {
	if i.IsDir() {
		return nil
	}
	return os.ErrInvalid
}

func (i *Inode) ReadDir(input *fuse.ReadIn, out *fuse.DirEntryList) error {
	return os.ErrInvalid
}
