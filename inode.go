package squashfs

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"log"
	"strings"
	"sync/atomic"
)

type Inode struct {
	// refcnt is first value to get guaranteed 64bits alignment, if not sync/atomic will panic
	refcnt uint64 // for fuse

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
	IdxCount   uint16 // index count for advanced directories
	XattrIdx   uint32 // xattr table index (if relevant)
	Sparse     uint64

	// fragment
	FragBlock uint32
	FragOfft  uint32

	// file blocks (some have value 0x1001000)
	Blocks     []uint32
	BlocksOfft []uint64
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

	// check index
	sb.inoIdxL.RLock()
	inor, ok := sb.inoIdx[uint32(ino)]
	sb.inoIdxL.RUnlock()
	if ok {
		return sb.GetInodeRef(inor)
	}

	// TODO locate inodeRef from the nfs export table

	log.Printf("get inode WIP %d", ino)
	return nil, fs.ErrInvalid
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
			if i.Blocks[block] == 0xffffffff {
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
			} else if i.Blocks[block] == 0 {
				// this part of the file contains only zeroes
				buf = make([]byte, i.sb.BlockSize)
			} else {
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

		log.Printf("load at block=%d offset=%d", block, offset)
	}
	return 0, fs.ErrInvalid
}

func (i *Inode) LookupRelativeInode(ctx context.Context, name string) (*Inode, error) {
	switch i.Type {
	case 1, 8:
		// basic dir, we need to iterate (cache data?)
		dr, err := i.sb.dirReader(i)
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

			if name == ename {
				// found
				found, err := i.sb.GetInodeRef(inoR)
				if err != nil {
					return nil, err
				}
				// cache
				i.sb.inoIdxL.Lock()
				i.sb.inoIdx[found.Ino] = inoR
				i.sb.inoIdxL.Unlock()
				// return
				return found, nil
			}
		}
	}
	log.Printf("squashfs: lookup name %s from inode %d TODO", name, i.Ino)
	return nil, fs.ErrInvalid
}

func (i *Inode) LookupRelativeInodePath(ctx context.Context, name string) (*Inode, error) {
	// similar to lookup, but handles slashes in name and returns an inode
	cur := i

	for {
		if len(name) == 0 {
			// trailing slash?
			return cur, nil
		}
		pos := strings.IndexByte(name, '/')
		if pos == -1 {
			// no /
			return cur.LookupRelativeInode(ctx, name)
		}
		if pos == 0 {
			// skip initial /
			name = name[1:]
			continue
		}
		t, err := cur.LookupRelativeInode(ctx, name[:pos])
		if err != nil {
			return nil, err
		}
		// found an inode
		cur = t
		name = name[pos+1:]
	}
}

func (i *Inode) Mode() fs.FileMode {
	return UnixToMode(uint32(i.Perm)) | squashfsTypeToMode(i.Type)
}

func (i *Inode) IsDir() bool {
	switch i.Type {
	case 1, 8:
		return true
	}
	return false
}

func (i *Inode) Readlink() ([]byte, error) {
	switch i.Type {
	case 3, 10:
		return i.SymTarget, nil
	}
	return nil, fs.ErrInvalid
}

func (i *Inode) AddRef(count uint64) uint64 {
	return atomic.AddUint64(&i.refcnt, count)
}

func (i *Inode) DelRef(count uint64) uint64 {
	return atomic.AddUint64(&i.refcnt, ^(count - 1))
}
