package squashfs

import (
	"git.atonline.com/azusa/apkg/apkgfs"
	"github.com/MagicalTux/go-fuse/fuse"
)

func (i *Inode) FillAttr(attr *fuse.Attr) error {
	attr.Size = i.Size
	attr.Blocks = 1
	attr.Mode = apkgfs.ModeToUnix(i.Mode())
	attr.Nlink = i.NLink // 1 required
	attr.Rdev = 1
	attr.Atime = uint64(i.ModTime)
	attr.Mtime = uint64(i.ModTime)
	attr.Ctime = uint64(i.ModTime)
	return nil
}
