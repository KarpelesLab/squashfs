package squashfs

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"log"
	"strings"
	"sync/atomic"
)

// Inode represents a file, directory, or other filesystem object in a SquashFS filesystem.
// It contains all the metadata and references to data blocks that make up the file's contents.
// Inodes implement various interfaces like io.ReaderAt to provide file-like access to their contents.
type Inode struct {
	// refcnt is first value to get guaranteed 64bits alignment, if not sync/atomic will panic
	refcnt uint64 // reference counter for FUSE support

	sb *Superblock // parent superblock

	Type    Type   // file type (regular file, directory, symlink, etc.)
	Perm    uint16 // permission bits
	UidIdx  uint16 // user ID index (in the ID table)
	GidIdx  uint16 // group ID index (in the ID table)
	ModTime int32  // modification time (Unix timestamp)
	Ino     uint32 // inode number (unique identifier)

	StartBlock uint64 // start of data blocks
	NLink      uint32 // number of hard links
	Size       uint64 // file size in bytes (interpretation varies by type)
	Offset     uint32 // offset within block (uint16 for directories)
	ParentIno  uint32 // parent directory's inode number (for directories)
	SymTarget  []byte // target path for symlinks
	IdxCount   uint16 // count of directory index entries (for extended directories)
	XattrIdx   uint32 // extended attribute index if present
	Sparse     uint64 // sparse file information

	// fragment information for file data that doesn't fill a complete block
	FragBlock uint32 // fragment block index
	FragOfft  uint32 // offset within fragment block

	// data block information
	Blocks     []uint32         // block sizes, possibly with compression flags
	BlocksOfft []uint64         // offsets for each block from StartBlock
	DirIndex   []*DirIndexEntry // directory index entries for fast lookups
}

func (sb *Superblock) GetInode(ino uint64) (*Inode, error) {
	if ino == 1 {
		// get root inode
		return sb.rootIno, nil
	}
	if ino == sb.rootInoN {
		// we reverse
		ino = 1
	}

	// check index cache
	inoR, ok := sb.getInodeRefCache(uint32(ino))
	if ok {
		return sb.GetInodeRef(inoR)
	}

	// we do not use the flags here, but only see if the table is present. If absent it will be all f's
	//if !sb.Flags.Has(EXPORTABLE) {
	if sb.ExportTableStart == ^uint64(0) {
		return nil, ErrInodeNotExported
	}

	// load the export table
	tr, err := sb.newTableReader(int64(sb.ExportTableStart), int(8*(ino-1)))
	if err != nil {
		return nil, err
	}

	err = binary.Read(tr, sb.order, &inoR)
	if err != nil {
		return nil, err
	}

	// cache value
	sb.setInodeRefCache(uint32(ino), inoR)

	return sb.GetInodeRef(inoR)
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

	//log.Printf("read inode #%d type=%d", ino.Ino, ino.Type)

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

		//log.Printf("squashfs: read basic directory success, parent=%d", ino.ParentIno)
	case 8: // Extended dir
		var u32 uint32
		var u16 uint16

		err = binary.Read(r, sb.order, &ino.NLink)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}
		ino.Size = uint64(u32)

		err = binary.Read(r, sb.order, &u32)
		if err != nil {
			return nil, err
		}
		ino.StartBlock = uint64(u32)

		err = binary.Read(r, sb.order, &ino.ParentIno)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &ino.IdxCount)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &u16)
		if err != nil {
			return nil, err
		}
		ino.Offset = uint32(u16)

		err = binary.Read(r, sb.order, &ino.XattrIdx)
		if err != nil {
			return nil, err
		}

		ino.DirIndex = make([]*DirIndexEntry, ino.IdxCount) // max 65536 as this is 16bits
		buf := make([]byte, 4*3)                            // read 4 u32 values
		for n := range ino.DirIndex {
			_, err = io.ReadFull(r, buf)
			if err != nil {
				return nil, err
			}
			di := &DirIndexEntry{
				Index: sb.order.Uint32(buf[:4]),
				Start: sb.order.Uint32(buf[4:8]),
			}
			nameLen := sb.order.Uint32(buf[8:12]) + 1
			if nameLen > 256 {
				// MAX_PATH is actually lower than that on most platforms
				return nil, errors.New("directory index contains name length >256")
			}
			name := make([]byte, sb.order.Uint32(buf[8:12])+1)
			_, err = io.ReadFull(r, name)
			if err != nil {
				return nil, err
			}
			di.Name = string(name)
			ino.DirIndex[n] = di
		}
		//log.Printf("squashfs: read extended directory success, parent=%d indexes=%d size=%d", ino.ParentIno, ino.IdxCount, ino.Size)
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
		//log.Printf("estimated %d blocks", blocks)

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

		if ino.FragBlock != 0xffffffff {
			// this has a fragment instead of last block
			ino.Blocks = append(ino.Blocks, 0xffffffff) // special code
		}
	case 9: // extended file
		err = binary.Read(r, sb.order, &ino.StartBlock)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &ino.Size)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &ino.Sparse) // TODO how to handle this?
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &ino.NLink)
		if err != nil {
			return nil, err
		}

		// fragment_block_index
		err = binary.Read(r, sb.order, &ino.FragBlock)
		if err != nil {
			return nil, err
		}
		err = binary.Read(r, sb.order, &ino.FragOfft)
		if err != nil {
			return nil, err
		}

		err = binary.Read(r, sb.order, &ino.XattrIdx)
		if err != nil {
			return nil, err
		}

		// try to find out how many block_sizes entries
		blocks := int(ino.Size / uint64(sb.BlockSize))
		if ino.FragBlock == 0xffffffff {
			// file does not end in a fragment
			if ino.Size%uint64(sb.BlockSize) != 0 {
				blocks += 1
			}
		}
		//log.Printf("estimated %d blocks", blocks)

		ino.Blocks = make([]uint32, blocks)
		ino.BlocksOfft = make([]uint64, blocks)
		var u32 uint32

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

		if ino.FragBlock != 0xffffffff {
			// this has a fragment instead of last block
			ino.Blocks = append(ino.Blocks, 0xffffffff) // special code
		}

		//log.Printf("squashfs: read extended file success, sparse=%d size=%d fragblock=%x", ino.Sparse, ino.Size, ino.FragBlock)
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

		//log.Printf("squashfs: read symlink to %s", ino.SymTarget)
	default:
		log.Printf("squashfs: unsupported inode type %d", ino.Type)
		return ino, nil
	}

	return ino, nil
}

func (i *Inode) ReadAt(p []byte, off int64) (int, error) {
	switch i.Type {
	case 2, 9: // Basic file
		//log.Printf("read request off=%d len=%d", off, len(p))

		if uint64(off) >= i.Size {
			// no read
			return 0, io.EOF
		}

		if uint64(off+int64(len(p))) > i.Size {
			p = p[:int64(i.Size)-off]
		}

		// we need to know what block to start with
		block := int(off / int64(i.sb.BlockSize))
		offset := int(off % int64(i.sb.BlockSize))
		n := 0

		for {
			var buf []byte

			// read block
			switch i.Blocks[block] {
			case 0xffffffff:
				// this is a fragment, need to decode fragment
				//log.Printf("frag table offset=%d", i.sb.FragTableStart)

				// read table offset
				sub := int64(i.FragBlock) / 512 * 8
				blInfo := make([]byte, 8)
				_, err := i.sb.fs.ReadAt(blInfo, int64(i.sb.FragTableStart)+sub)
				if err != nil {
					return n, err
				}

				// read table
				t, err := i.sb.newTableReader(int64(i.sb.order.Uint64(blInfo)), int(i.FragBlock%512)*16)
				if err != nil {
					return n, err
				}

				//log.Printf("fragment blinfo=%v", blInfo)
				var start uint64
				var size uint32
				err = binary.Read(t, i.sb.order, &start)
				if err != nil {
					return n, err
				}
				err = binary.Read(t, i.sb.order, &size)
				if err != nil {
					return n, err
				}

				//log.Printf("fragment at %d:%d => start=0x%x (size=0x%x) len=%d", i.FragBlock, i.FragOfft, start, size, len(p))

				if size&0x1000000 == 0x1000000 {
					// no compression
					buf = make([]byte, size&(0x1000000-1))
					_, err = i.sb.fs.ReadAt(buf, int64(start))
					if err != nil {
						return n, err
					}
				} else {
					// read fragment
					buf = make([]byte, size)
					_, err = i.sb.fs.ReadAt(buf, int64(start))
					if err != nil {
						return n, err
					}

					// decompress
					buf, err = i.sb.Comp.decompress(buf)
					if err != nil {
						return n, err
					}
				}

				if i.FragOfft != 0 {
					buf = buf[i.FragOfft:]
				}
			case 0:
				// this part of the file contains only zeroes
				buf = make([]byte, i.sb.BlockSize)
			default:
				buf = make([]byte, i.Blocks[block]&0xfffff)
				_, err := i.sb.fs.ReadAt(buf, int64(i.StartBlock+i.BlocksOfft[block]))
				if err != nil {
					return n, err
				}

				// check for compression
				if i.Blocks[block]&0x1000000 == 0 {
					// compressed
					buf, err = i.sb.Comp.decompress(buf)
					if err != nil {
						return n, err
					}
				}
			}

			// check offset
			if offset > 0 {
				buf = buf[offset:]
			}

			// copy
			l := copy(p, buf)
			n += l
			if l == len(p) {
				// end of copy
				return n, nil
			}

			// advance out ptr
			p = p[l:]

			// next block
			block += 1
			offset = 0
		}
	}
	return 0, fs.ErrInvalid
}

// lookupRelativeInode finds the given inode in the directory
func (i *Inode) lookupRelativeInode(name string) (*Inode, error) {
	// Special case for "." - return the current inode
	if name == "." {
		return i, nil
	}

	// Handle directory lookups
	switch i.Type {
	case 1, 8:
		// basic/extended dir, we need to iterate (cache data?)
		var di *DirIndexEntry
		for _, t := range i.DirIndex {
			if strings.Compare(name, t.Name) < 0 {
				// went too far or no index (ie. basic dir)
				break
			}
			di = t
		}
		dr, err := i.sb.dirReader(i, di)
		if err != nil {
			return nil, err
		}
		for {
			ename, inoR, err := dr.next()
			if err != nil {
				if err == io.EOF {
					return nil, fs.ErrNotExist
				}
				return nil, err
			}
			if di != nil && ename > name {
				// if the dir is indexed and we're past our lookup, it means the file does not exist
				return nil, fs.ErrNotExist
			}

			if name == ename {
				// found, load the inode from its ref
				found, err := i.sb.GetInodeRef(inoR)
				if err != nil {
					return nil, err
				}
				// cache info
				i.sb.setInodeRefCache(found.Ino, inoR)
				// return
				return found, nil
			}
		}
	}
	return nil, fs.ErrInvalid
}

// Mode returns the inode's mode as fs.FileMode
func (i *Inode) Mode() fs.FileMode {
	return unixToMode(uint32(i.Perm)) | i.Type.Mode()
}

// IsDir returns true if the inode is a directory inode.
func (i *Inode) IsDir() bool {
	switch i.Type {
	case 1, 8:
		return true
	}
	return false
}

// Readlink returns the inode's link
func (i *Inode) Readlink() ([]byte, error) {
	switch i.Type {
	case 3, 10:
		return i.SymTarget, nil
	}
	return nil, fs.ErrInvalid
}

// AddRef atomatically increments the inode's refcount and returns the new value. This is mainly useful when
// using fuse and can be safely ignored.
func (i *Inode) AddRef(count uint64) uint64 {
	return atomic.AddUint64(&i.refcnt, count)
}

// DelRef atomatically decrements the inode's refcount and returns the new value. This is mainly useful when
// using fuse and can be safely ignored.
func (i *Inode) DelRef(count uint64) uint64 {
	return atomic.AddUint64(&i.refcnt, ^(count - 1))
}

// GetUid returns inode's owner uid, or zero if an error happens
func (i *Inode) GetUid() uint32 {
	if len(i.sb.idTable) >= int(i.UidIdx) {
		return i.sb.idTable[i.UidIdx]
	}
	return 0
}

// GetGid returns inode's group id, or zero if an error happens
func (i *Inode) GetGid() uint32 {
	if len(i.sb.idTable) >= int(i.GidIdx) {
		return i.sb.idTable[i.GidIdx]
	}
	return 0
}
