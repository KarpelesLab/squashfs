//go:build fuse

package squashfs

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func (i *Inode) Lookup(ctx context.Context, name string) (uint64, error) {
	res, err := i.LookupRelativeInode(ctx, name)
	if err != nil {
		return 0, err
	}
	return res.publicInodeNum(), nil
}

func (i *Inode) Open(flags uint32) (uint32, error) {
	// always ok :)
	// Tell fuse to cache the open (squashfs is readonly so not likely to change)
	return fuse.FOPEN_KEEP_CACHE, nil
}

func (i *Inode) OpenDir() (uint32, error) {
	if i.IsDir() {
		// only allow open if IsDir is true
		return fuse.FOPEN_KEEP_CACHE, nil
	}
	return 0, os.ErrInvalid
}

// publicInodeNum returns a inode number suitable for use in mounts sharing multiple squashfs images. The root is
// required to be inode 1, so in case it is not the case we swap the root inode number with whatever inode it was
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

// fillEntry files a fuse.EntryOut structure with the appropriate information
func (i *Inode) fillEntry(entry *fuse.EntryOut) {
	entry.NodeId = i.publicInodeNum()
	entry.Attr.Ino = entry.NodeId
	i.FillAttr(&entry.Attr)
	entry.SetEntryTimeout(time.Second)
	entry.SetAttrTimeout(time.Second)
}

func (i *Inode) ReadDir(input *fuse.ReadIn, out *fuse.DirEntryList, plus bool) error {
	pos := input.Offset + 1
	//log.Printf("readdir offset %d", input.Offset)

	switch i.Type {
	case 1, 8:
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
