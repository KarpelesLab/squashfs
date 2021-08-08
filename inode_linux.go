package squashfs

import (
	"git.atonline.com/azusa/apkg/apkgfs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func (i *Inode) FillAttr(attr *fuse.Attr) error {
	attr.Size = i.Size
	attr.Blocks = uint64(len(i.Blocks)) + 1
	attr.Mode = apkgfs.ModeToUnix(i.Mode())
	attr.Nlink = i.NLink // 1 required
	attr.Rdev = 1
	attr.Blksize = i.sb.BlockSize
	attr.Atime = uint64(i.ModTime)
	attr.Mtime = uint64(i.ModTime)
	attr.Ctime = uint64(i.ModTime)
	// fill uid/gid based on idtable
	if len(i.sb.idTable) >= int(i.UidIdx) {
		attr.Owner.Uid = i.sb.idTable[i.UidIdx]
	}
	if len(i.sb.idTable) >= int(i.GidIdx) {
		attr.Owner.Gid = i.sb.idTable[i.GidIdx]
	}
	return nil
}
