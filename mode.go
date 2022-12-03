package squashfs

import (
	"io/fs"
)

// squashfs internal modes are based on linux, so use these methods:
// based on: https://golang.org/src/os/stat_linux.go

const (
	S_IFMT   = 0xf000
	S_IFREG  = 0x8000
	S_IFDIR  = 0x4000
	S_IFBLK  = 0x6000
	S_IFCHR  = 0x2000
	S_IFIFO  = 0x1000
	S_IFLNK  = 0xa000
	S_IFSOCK = 0xc000

	S_ISVTX = 0x200
	S_ISGID = 0x400
	S_ISUID = 0x800

	S_IRUSR = 0x100
	S_IRGRP = 0x20
	S_IROTH = 0x4

	S_IWUSR = 0x80
	S_IWGRP = 0x10
	S_IWOTH = 0x2

	S_IXUSR = 0x40
	S_IXGRP = 0x8
	S_IXOTH = 0x1
)

func UnixToMode(mode uint32) fs.FileMode {
	res := fs.FileMode(mode & 0777)

	switch {
	case mode&S_IFCHR == S_IFCHR:
		res |= fs.ModeCharDevice
	case mode&S_IFBLK == S_IFBLK:
		res |= fs.ModeDevice
	case mode&S_IFDIR == S_IFDIR:
		res |= fs.ModeDir
	case mode&S_IFIFO == S_IFIFO:
		res |= fs.ModeNamedPipe
	case mode&S_IFLNK == S_IFLNK:
		res |= fs.ModeSymlink
	case mode&S_IFSOCK == S_IFSOCK:
		res |= fs.ModeSocket
	}

	// extra flags
	if mode&S_ISGID == S_ISGID {
		res |= fs.ModeSetgid
	}

	if mode&S_ISUID == S_ISUID {
		res |= fs.ModeSetuid
	}

	if mode&S_ISVTX == S_ISVTX {
		res |= fs.ModeSticky
	}

	return res
}

func ModeToUnix(mode fs.FileMode) uint32 {
	res := uint32(mode.Perm())

	// type of file
	switch {
	case mode&fs.ModeCharDevice == fs.ModeCharDevice:
		res |= S_IFCHR
	case mode&fs.ModeDevice == fs.ModeDevice:
		res |= S_IFBLK
	case mode&fs.ModeDir == fs.ModeDir:
		res |= S_IFDIR
	case mode&fs.ModeNamedPipe == fs.ModeNamedPipe:
		res |= S_IFIFO
	case mode&fs.ModeSymlink == fs.ModeSymlink:
		res |= S_IFLNK
	case mode&fs.ModeSocket == fs.ModeSocket:
		res |= S_IFSOCK
	default:
		res |= S_IFREG
	}

	// extra flags
	if mode&fs.ModeSetgid == fs.ModeSetgid {
		res |= S_ISGID
	}

	if mode&fs.ModeSetuid == fs.ModeSetuid {
		res |= S_ISUID
	}

	if mode&fs.ModeSticky == fs.ModeSticky {
		res |= S_ISVTX
	}

	return res
}
