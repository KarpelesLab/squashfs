package squashfs

import (
	"io/fs"
)

// squashfs internal modes are based on linux, so use these methods:
// based on: https://golang.org/src/os/stat_linux.go

const (
	_S_IFMT   = 0xf000
	_S_IFREG  = 0x8000
	_S_IFDIR  = 0x4000
	_S_IFBLK  = 0x6000
	_S_IFCHR  = 0x2000
	_S_IFIFO  = 0x1000
	_S_IFLNK  = 0xa000
	_S_IFSOCK = 0xc000

	_S_ISVTX = 0x200
	_S_ISGID = 0x400
	_S_ISUID = 0x800

	_S_IRUSR = 0x100
	_S_IRGRP = 0x20
	_S_IROTH = 0x4

	_S_IWUSR = 0x80
	_S_IWGRP = 0x10
	_S_IWOTH = 0x2

	_S_IXUSR = 0x40
	_S_IXGRP = 0x8
	_S_IXOTH = 0x1
)

func unixToMode(mode uint32) fs.FileMode {
	res := fs.FileMode(mode & 0777)

	switch {
	case mode&_S_IFCHR == _S_IFCHR:
		res |= fs.ModeCharDevice
	case mode&_S_IFBLK == _S_IFBLK:
		res |= fs.ModeDevice
	case mode&_S_IFDIR == _S_IFDIR:
		res |= fs.ModeDir
	case mode&_S_IFIFO == _S_IFIFO:
		res |= fs.ModeNamedPipe
	case mode&_S_IFLNK == _S_IFLNK:
		res |= fs.ModeSymlink
	case mode&_S_IFSOCK == _S_IFSOCK:
		res |= fs.ModeSocket
	}

	// extra flags
	if mode&_S_ISGID == _S_ISGID {
		res |= fs.ModeSetgid
	}

	if mode&_S_ISUID == _S_ISUID {
		res |= fs.ModeSetuid
	}

	if mode&_S_ISVTX == _S_ISVTX {
		res |= fs.ModeSticky
	}

	return res
}

func modeToUnix(mode fs.FileMode) uint32 {
	res := uint32(mode.Perm())

	// type of file
	switch {
	case mode&fs.ModeCharDevice == fs.ModeCharDevice:
		res |= _S_IFCHR
	case mode&fs.ModeDevice == fs.ModeDevice:
		res |= _S_IFBLK
	case mode&fs.ModeDir == fs.ModeDir:
		res |= _S_IFDIR
	case mode&fs.ModeNamedPipe == fs.ModeNamedPipe:
		res |= _S_IFIFO
	case mode&fs.ModeSymlink == fs.ModeSymlink:
		res |= _S_IFLNK
	case mode&fs.ModeSocket == fs.ModeSocket:
		res |= _S_IFSOCK
	default:
		res |= _S_IFREG
	}

	// extra flags
	if mode&fs.ModeSetgid == fs.ModeSetgid {
		res |= _S_ISGID
	}

	if mode&fs.ModeSetuid == fs.ModeSetuid {
		res |= _S_ISUID
	}

	if mode&fs.ModeSticky == fs.ModeSticky {
		res |= _S_ISVTX
	}

	return res
}
