package squashfs

import "errors"

var (
	ErrInvalidFile      = errors.New("invalid file, squashfs signature not found")
	ErrInvalidSuper     = errors.New("invalid squashfs superblock")
	ErrInvalidVersion   = errors.New("invalid file version, expected squashfs 4.0")
	ErrInodeNotExported = errors.New("unknown squashfs inode and no NFS export table")
	ErrNotDirectory     = errors.New("Not a directory")
	ErrTooManySymlinks  = errors.New("Too many levels of symbolic links")
)
