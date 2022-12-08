package squashfs

import "io/fs"

type Type uint16

const (
	DirType Type = iota + 1
	FileType
	SymlinkType
	BlockDevType
	CharDevType
	FifoType
	SocketType
	XDirType
	XFileType
	XSymlinkType
	XBlockDevType
	XCharDevType
	XFifoType
	XSocketType
)

// Basic returns the type as a basic type (ie. XDirType.Basic() == DirType)
func (t Type) Basic() Type {
	if t >= 8 {
		return t - 7
	}
	return t
}

func (t Type) IsDir() bool {
	return t.Basic() == DirType
}

func (t Type) IsSymlink() bool {
	return t.Basic() == SymlinkType
}

// Mode returns a fs.FileMode for this type that contains no permissions, only the file's type
func (t Type) Mode() fs.FileMode {
	switch t.Basic() {
	case DirType:
		return fs.ModeDir
	case FileType:
		return 0
	case SymlinkType:
		return fs.ModeSymlink
	case BlockDevType:
		return fs.ModeDevice // block device
	case CharDevType:
		return fs.ModeDevice | fs.ModeCharDevice // char device
	case FifoType:
		return fs.ModeNamedPipe
	case SocketType:
		return fs.ModeSocket
	default:
		// ??
		return fs.ModeIrregular
	}
}
