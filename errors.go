package squashfs

import "errors"

// Package-specific error variables that can be used with errors.Is() for error handling.
var (
	// ErrInvalidFile is returned when the file format is not recognized as SquashFS
	ErrInvalidFile = errors.New("invalid file, squashfs signature not found")

	// ErrInvalidSuper is returned when the superblock data is corrupted or invalid
	ErrInvalidSuper = errors.New("invalid squashfs superblock")

	// ErrInvalidVersion is returned when the SquashFS version is not 4.0
	// This library only supports SquashFS 4.0 format
	ErrInvalidVersion = errors.New("invalid file version, expected squashfs 4.0")

	// ErrInodeNotExported is returned when trying to access an inode that isn't in the export table
	ErrInodeNotExported = errors.New("unknown squashfs inode and no NFS export table")

	// ErrNotDirectory is returned when attempting to perform directory operations on a non-directory
	ErrNotDirectory = errors.New("not a directory")

	// ErrTooManySymlinks is returned when symlink resolution exceeds the maximum depth
	// This prevents infinite loops in symlink resolution
	ErrTooManySymlinks = errors.New("too many levels of symbolic links")
)
