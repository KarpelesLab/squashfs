package squashfs

import (
	"encoding/binary"
	"io"
	"io/fs"
	"time"
)

// Writer creates SquashFS filesystem images.
// It builds the filesystem structure in memory and streams the final
// image to an io.Writer when Finalize() is called.
type Writer struct {
	w io.Writer

	// Filesystem metadata
	blockSize uint32
	comp      Compression
	modTime   int32

	// In-memory inode tree
	inodes     []*writerInode
	rootInode  *writerInode
	inodeCount uint32

	// Data tracking
	idTable map[uint32]uint32 // uid/gid -> index mapping
	idList  []uint32          // ordered list of uid/gid values
}

// writerInode represents an inode being built in memory
type writerInode struct {
	path     string
	name     string
	ino      uint32
	parent   *writerInode
	children []*writerInode

	// File metadata
	mode     fs.FileMode
	size     uint64
	modTime  int64
	uid      uint32
	gid      uint32
	nlink    uint32
	fileType Type

	// For regular files
	data io.Reader

	// For symlinks
	linkTarget string

	// For directories
	entries []*writerInode
}

// WriterOption configures a Writer
type WriterOption func(*Writer) error

// WithBlockSize sets the block size for the filesystem (default: 131072)
func WithBlockSize(size uint32) WriterOption {
	return func(w *Writer) error {
		w.blockSize = size
		return nil
	}
}

// WithCompression sets the compression type (default: GZip)
func WithCompression(comp Compression) WriterOption {
	return func(w *Writer) error {
		w.comp = comp
		return nil
	}
}

// WithModTime sets the filesystem modification time (default: current time)
func WithModTime(t time.Time) WriterOption {
	return func(w *Writer) error {
		w.modTime = int32(t.Unix())
		return nil
	}
}

// NewWriter creates a new SquashFS writer that will write to w.
// The filesystem is built in memory and written when Finalize() is called.
func NewWriter(w io.Writer, opts ...WriterOption) (*Writer, error) {
	writer := &Writer{
		w:         w,
		blockSize: 131072, // 128KB default
		comp:      GZip,
		modTime:   int32(time.Now().Unix()),
		idTable:   make(map[uint32]uint32),
		inodes:    make([]*writerInode, 0),
	}

	// Create root inode
	writer.rootInode = &writerInode{
		path:     "",
		name:     "",
		ino:      1,
		mode:     fs.ModeDir | 0755,
		modTime:  time.Now().Unix(),
		uid:      0,
		gid:      0,
		nlink:    2,
		fileType: DirType,
		entries:  make([]*writerInode, 0),
	}
	writer.inodes = append(writer.inodes, writer.rootInode)
	writer.inodeCount = 1

	// Apply options
	for _, opt := range opts {
		if err := opt(writer); err != nil {
			return nil, err
		}
	}

	return writer, nil
}

// Add adds a file or directory to the filesystem.
// This method is compatible with fs.WalkDirFunc, allowing it to be used directly
// with fs.WalkDir:
//
//	err := fs.WalkDir(srcFS, ".", writer.Add)
//
// The actual file data is not written until Finalize() is called.
func (w *Writer) Add(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}

	// Skip root (already created)
	if path == "." || path == "" {
		return nil
	}

	info, err := d.Info()
	if err != nil {
		return err
	}

	w.inodeCount++
	inode := &writerInode{
		path:    path,
		name:    info.Name(),
		ino:     w.inodeCount,
		mode:    info.Mode(),
		size:    uint64(info.Size()),
		modTime: info.ModTime().Unix(),
		nlink:   1,
	}

	// TODO: Extract uid/gid from info.Sys() if available

	// Determine inode type
	switch {
	case info.Mode().IsDir():
		inode.fileType = DirType
		inode.entries = make([]*writerInode, 0)
		inode.nlink = 2
	case info.Mode().IsRegular():
		inode.fileType = FileType
		// TODO: Store reference to read file data later
	case info.Mode()&fs.ModeSymlink != 0:
		inode.fileType = SymlinkType
		// TODO: Read symlink target
	default:
		// TODO: Handle other file types (char, block, fifo, socket)
		inode.fileType = FileType // treat as regular file for now
	}

	// Add to inode list
	w.inodes = append(w.inodes, inode)

	// TODO: Build directory tree structure

	return nil
}

// Finalize writes the complete SquashFS filesystem to the underlying writer.
// After this method returns, the filesystem image is complete and the Writer
// should not be used again.
func (w *Writer) Finalize() error {
	// TODO: Implement the actual writing logic:
	// 1. Write data blocks (compressed)
	// 2. Write inode table
	// 3. Write directory table
	// 4. Write fragment table
	// 5. Write export table
	// 6. Write UID/GID table
	// 7. Write superblock at the beginning

	// For now, just write a basic superblock structure
	sb := make([]byte, SuperblockSize)
	order := binary.LittleEndian

	// Magic
	order.PutUint32(sb[0:4], 0x73717368)
	// Inode count
	order.PutUint32(sb[4:8], w.inodeCount)
	// Mod time
	order.PutUint32(sb[8:12], uint32(w.modTime))
	// Block size
	order.PutUint32(sb[12:16], w.blockSize)
	// Compression
	order.PutUint16(sb[20:22], uint16(w.comp))
	// Version
	order.PutUint16(sb[28:30], 4) // major
	order.PutUint16(sb[30:32], 0) // minor

	_, err := w.w.Write(sb)
	return err
}
