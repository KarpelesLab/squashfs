package squashfs

import "io/fs"

// Type represents the type of a file or directory in a SquashFS filesystem.
// SquashFS has both basic and extended types, where extended types provide additional
// information or capabilities.
type Type uint16

const (
	DirType       Type = iota + 1 // Basic directory
	FileType                      // Basic regular file
	SymlinkType                   // Symbolic link
	BlockDevType                  // Block device
	CharDevType                   // Character device
	FifoType                      // Named pipe (FIFO)
	SocketType                    // UNIX domain socket
	XDirType                      // Extended directory with optional index information
	XFileType                     // Extended file with additional metadata
	XSymlinkType                  // Extended symbolic link
	XBlockDevType                 // Extended block device
	XCharDevType                  // Extended character device
	XFifoType                     // Extended named pipe
	XSocketType                   // Extended UNIX domain socket
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
