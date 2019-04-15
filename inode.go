package squashfs

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"os"
	"time"

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
	SymTarget  []byte // The target path this symlink points to

	// fragment
	FragBlock uint32
	FragOfft  uint32

	// file blocks (some have value 0x1001000)
	Blocks     []uint32
	BlocksOfft []uint64
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

	// check index
	sb.inoIdxL.RLock()
	inor, ok := sb.inoIdx[uint32(ino)]
	sb.inoIdxL.RUnlock()
	if ok {
		return sb.GetInodeRef(inor)
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
	case 2: // Basic file
		var u32 uint32
		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}
		ino.StartBlock = uint64(u32)

		// fragment_block_index
		err = binary.Read(r, sb.order, &ino.FragBlock)
		if err != nil {
			return nil, err
		}
		err = binary.Read(r, sb.order, &ino.FragOfft)
		if err != nil {
			return nil, err
		}
		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}
		ino.Size = uint64(u32)

		// try to find out how many block_sizes entries
		blocks := int(ino.Size / uint64(sb.BlockSize))
		if ino.FragBlock == 0xffffffff {
			// file does not end in a fragment
			if ino.Size%uint64(sb.BlockSize) != 0 {
				blocks += 1
			}
		}
		log.Printf("estimated %d blocks", blocks)

		ino.Blocks = make([]uint32, blocks)
		ino.BlocksOfft = make([]uint64, blocks)

		offt := uint64(0)

		// read blocks
		for i := 0; i < blocks; i += 1 {
			err = binary.Read(r, sb.order, &u32)
			if err != nil {
				return nil, err
			}

			ino.Blocks[i] = u32
			ino.BlocksOfft[i] = offt
			offt += uint64(u32) & 0xfffff // 1MB-1, since max block size is 1MB
		}
	case 3: // basic symlink
		err = binary.Read(r, sb.order, &ino.NLink)
		if err != nil {
			return nil, err
		}

		// read symlink target length
		var u32 uint32
		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}

		if u32 > 4096 {
			// why is symlink length even stored as u32 ?
			return nil, errors.New("symlink target too long")
		}
		ino.Size = uint64(u32)

		// buffer
		buf := make([]byte, u32)
		_, err = io.ReadFull(r, buf)
		if err != nil {
			return nil, err
		}
		ino.SymTarget = buf

		log.Printf("squashfs: read symlink to %s", ino.SymTarget)
	default:
		log.Printf("squashfs: unsupported inode type %d", ino.Type)
		return ino, nil
	}

	return ino, nil
}

func (i *Inode) Lookup(name string) (uint64, error) {
	switch i.Type {
	case 1:
		// basic dir, we need to iterate (cache data?)
		dr, err := i.sb.dirReader(i)
		if err != nil {
			return 0, err
		}
		for {
			ename, inoR, err := dr.next()
			if err != nil {
				if err == io.EOF {
					return 0, os.ErrNotExist
				}
				return 0, err
			}

			if name == ename {
				// found
				found, err := i.sb.GetInodeRef(inoR)
				if err != nil {
					return 0, err
				}
				// cache
				i.sb.inoIdxL.Lock()
				i.sb.inoIdx[found.Ino] = inoR
				i.sb.inoIdxL.Unlock()
				// return
				return found.publicInodeNum(), nil
			}
		}
	}
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
	switch i.Type {
	case 3, 10:
		return i.SymTarget, nil
	}
	return nil, os.ErrInvalid
}

func (i *Inode) Open(flags uint32) error {
	// ok :)
	return nil
}

func (i *Inode) OpenDir() error {
	if i.IsDir() {
		return nil
	}
	return os.ErrInvalid
}

func (i *Inode) publicInodeNum() uint64 {
	// compute inode number suitable for public
	if i.Ino == uint32(i.sb.rootInoN) {
		// we are the root inode, return 1
		return 1 + i.sb.inoOfft
	} else if i.Ino == 1 {
		// we are inode #1, return rootInoN
		return i.sb.rootInoN + i.sb.inoOfft
	} else {
		return uint64(i.Ino) + i.sb.inoOfft
	}
}

func (i *Inode) fillEntry(entry *fuse.EntryOut) {
	entry.NodeId = i.publicInodeNum()
	entry.Attr.Ino = entry.NodeId
	i.FillAttr(&entry.Attr)
	entry.SetEntryTimeout(time.Second)
	entry.SetAttrTimeout(time.Second)
}

func (i *Inode) ReadDir(input *fuse.ReadIn, out *fuse.DirEntryList, plus bool) error {
	pos := input.Offset + 1
	log.Printf("readdir offset %d", input.Offset)

	switch i.Type {
	case 1:
		// basic dir
		dr, err := i.sb.dirReader(i)
		if err != nil {
			return err
		}
		var name string
		var inoR inodeRef

		cur := uint64(0)
		for {
			cur += 1
			if cur > 2 {
				name, inoR, err = dr.next()
				if err != nil {
					if err == io.EOF {
						return nil
					}
					return err
				}
			}
			if cur < pos {
				continue
			}
			if cur == 1 {
				// .
				if !plus {
					if !out.Add(0, ".", uint64(i.Ino)+i.sb.inoOfft, uint32(i.Perm)) {
						return nil
					}
				} else {
					entry := out.AddDirLookupEntry(fuse.DirEntry{Mode: uint32(i.Perm), Name: ".", Ino: i.publicInodeNum()})
					if entry == nil {
						return nil
					}
					i.fillEntry(entry)
				}
				continue
			}
			if cur == 2 {
				// ..
				// TODO: return attributes for the actual parent?
				if !plus {
					if !out.Add(0, "..", uint64(i.Ino), uint32(i.Perm)) {
						return nil
					}
				} else {
					entry := out.AddDirLookupEntry(fuse.DirEntry{Mode: uint32(i.Perm), Name: "..", Ino: i.publicInodeNum()})
					if entry == nil {
						return nil
					}
					i.fillEntry(entry)
				}
				continue
			}

			// make inode ref
			ino, err := i.sb.GetInodeRef(inoR)
			if err != nil {
				log.Printf("failed to load inode: %s")
				return err
			}

			i.sb.inoIdxL.Lock()
			i.sb.inoIdx[ino.Ino] = inoR
			i.sb.inoIdxL.Unlock()

			if !plus {
				if !out.Add(0, string(name), ino.publicInodeNum(), uint32(ino.Perm)) {
					return nil
				}
			} else {
				entry := out.AddDirLookupEntry(fuse.DirEntry{Mode: uint32(ino.Perm), Name: string(name), Ino: ino.publicInodeNum()})
				if entry == nil {
					return nil
				}
				ino.fillEntry(entry)
			}
		}
	}
	return os.ErrInvalid
}
