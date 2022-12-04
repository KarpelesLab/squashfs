//go:build darwin && fuse

package squashfs

import (
	"github.com/hanwen/go-fuse/v2/fuse"
)

func (i *Inode) FillAttr(attr *fuse.Attr) error {
	attr.Size = i.Size
	attr.Blocks = uint64(len(i.Blocks)) + 1
	attr.Mode = ModeToUnix(i.Mode())
	attr.Nlink = i.NLink // 1 required
	attr.Rdev = 1
	attr.Atime = uint64(i.ModTime)
	attr.Mtime = uint64(i.ModTime)
	attr.Ctime = uint64(i.ModTime)
	return nil
}
