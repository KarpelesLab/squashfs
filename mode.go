package squashfs

import (
	"os"
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

func UnixToMode(mode uint32) os.FileMode {
	res := os.FileMode(mode & 0777)

	switch {
	case mode&S_IFCHR == S_IFCHR:
		res |= os.ModeCharDevice
	case mode&S_IFBLK == S_IFBLK:
		res |= os.ModeDevice
	case mode&S_IFDIR == S_IFDIR:
		res |= os.ModeDir
	case mode&S_IFIFO == S_IFIFO:
		res |= os.ModeNamedPipe
	case mode&S_IFLNK == S_IFLNK:
		res |= os.ModeSymlink
	case mode&S_IFSOCK == S_IFSOCK:
		res |= os.ModeSocket
	}

	// extra flags
	if mode&S_ISGID == S_ISGID {
		res |= os.ModeSetgid
	}

	if mode&S_ISUID == S_ISUID {
		res |= os.ModeSetuid
	}

	if mode&S_ISVTX == S_ISVTX {
		res |= os.ModeSticky
	}

	return res
}

func ModeToUnix(mode os.FileMode) uint32 {
	res := uint32(mode.Perm())

	// type of file
	switch {
	case mode&os.ModeCharDevice == os.ModeCharDevice:
		res |= S_IFCHR
	case mode&os.ModeDevice == os.ModeDevice:
		res |= S_IFBLK
	case mode&os.ModeDir == os.ModeDir:
		res |= S_IFDIR
	case mode&os.ModeNamedPipe == os.ModeNamedPipe:
		res |= S_IFIFO
	case mode&os.ModeSymlink == os.ModeSymlink:
		res |= S_IFLNK
	case mode&os.ModeSocket == os.ModeSocket:
		res |= S_IFSOCK
	default:
		res |= S_IFREG
	}

	// extra flags
	if mode&os.ModeSetgid == os.ModeSetgid {
		res |= S_ISGID
	}

	if mode&os.ModeSetuid == os.ModeSetuid {
		res |= S_ISUID
	}

	if mode&os.ModeSticky == os.ModeSticky {
		res |= S_ISVTX
	}

	return res
}
