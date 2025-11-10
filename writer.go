package squashfs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"time"
)

// Writer creates SquashFS filesystem images.
// It builds the filesystem structure in memory and streams the final
// image to an io.Writer when Finalize() is called.
type Writer struct {
	w          io.Writer
	wa         io.WriterAt // set if w implements WriterAt
	buf        *bytes.Buffer // used when w doesn't implement WriterAt
	offset     uint64        // current write offset

	// Filesystem metadata
	blockSize uint32
	comp      Compression
	modTime   int32
	flags     Flags

	// In-memory inode tree
	inodes     []*writerInode
	rootInode  *writerInode
	inodeCount uint32

	// Data tracking
	idTable map[uint32]uint32 // uid/gid -> index mapping
	idList  []uint32          // ordered list of uid/gid values

	// Table positions (filled during Finalize)
	idTableStart      uint64
	inodeTableStart   uint64
	dirTableStart     uint64
	fragTableStart    uint64
	exportTableStart  uint64
	xattrIdTableStart uint64
	bytesUsed         uint64
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
//
// If w implements io.WriterAt, the writer will write a blank superblock
// initially and update it at the end. Otherwise, it will buffer everything
// in memory and write it all at once when Finalize() is called.
func NewWriter(w io.Writer, opts ...WriterOption) (*Writer, error) {
	writer := &Writer{
		w:         w,
		blockSize: 131072, // 128KB default
		comp:      GZip,
		modTime:   int32(time.Now().Unix()),
		idTable:   make(map[uint32]uint32),
		inodes:    make([]*writerInode, 0),
	}

	// Check if writer supports WriterAt
	if wa, ok := w.(io.WriterAt); ok {
		writer.wa = wa
		writer.offset = SuperblockSize // start after superblock
	} else {
		// Use internal buffer - pre-allocate superblock space
		writer.buf = &bytes.Buffer{}
		// Write blank superblock placeholder
		writer.buf.Write(make([]byte, SuperblockSize))
		writer.offset = SuperblockSize
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

// SetCompression sets the compression algorithm to use when writing the filesystem.
// This can be called at any time before Finalize() is called.
// The compression affects metadata blocks and data blocks.
func (w *Writer) SetCompression(comp Compression) {
	w.comp = comp
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

// write writes data to the current offset and advances the offset.
func (w *Writer) write(data []byte) error {
	if w.wa != nil {
		// Use WriterAt
		_, err := w.wa.WriteAt(data, int64(w.offset))
		if err != nil {
			return err
		}
	} else {
		// Use buffer
		_, err := w.buf.Write(data)
		if err != nil {
			return err
		}
	}
	w.offset += uint64(len(data))
	return nil
}

// buildIDTable builds the unique UID/GID table and returns it
func (w *Writer) buildIDTable() error {
	// Collect all unique UIDs and GIDs
	seen := make(map[uint32]bool)
	w.idList = make([]uint32, 0)

	for _, inode := range w.inodes {
		if !seen[inode.uid] {
			seen[inode.uid] = true
			w.idList = append(w.idList, inode.uid)
		}
		if !seen[inode.gid] {
			seen[inode.gid] = true
			w.idList = append(w.idList, inode.gid)
		}
	}

	// Build index map
	for i, id := range w.idList {
		w.idTable[id] = uint32(i)
	}

	return nil
}

// writeMetadataBlock writes a metadata block with optional compression
// Returns the offset where the block was written
func (w *Writer) writeMetadataBlock(data []byte) (uint64, error) {
	blockStart := w.offset

	compressed, err := w.comp.compress(data)
	if err != nil || len(compressed) >= len(data) {
		// Compression failed or didn't save space - write uncompressed
		header := make([]byte, 2)
		binary.LittleEndian.PutUint16(header, uint16(len(data))|0x8000) // 0x8000 = uncompressed flag
		if err := w.write(header); err != nil {
			return 0, err
		}
		if err := w.write(data); err != nil {
			return 0, err
		}
	} else {
		// Write compressed
		header := make([]byte, 2)
		binary.LittleEndian.PutUint16(header, uint16(len(compressed)))
		if err := w.write(header); err != nil {
			return 0, err
		}
		if err := w.write(compressed); err != nil {
			return 0, err
		}
	}

	return blockStart, nil
}

// writeIDTable writes the UID/GID table using indirect table format
func (w *Writer) writeIDTable() error {
	// Build ID data
	idData := make([]byte, len(w.idList)*4)
	for i, id := range w.idList {
		binary.LittleEndian.PutUint32(idData[i*4:], id)
	}

	// Write the metadata block containing the ID data
	metadataBlockStart, err := w.writeMetadataBlock(idData)
	if err != nil {
		return err
	}

	// Now write the indirect table (array of pointers)
	w.idTableStart = w.offset

	// Single uint64 pointer to the metadata block
	pointer := make([]byte, 8)
	binary.LittleEndian.PutUint64(pointer, metadataBlockStart)
	return w.write(pointer)
}

// serializeInode serializes an inode to bytes (Basic Directory type only for now)
func (w *Writer) serializeInode(ino *writerInode) ([]byte, error) {
	buf := &bytes.Buffer{}
	order := binary.LittleEndian

	// Common inode header
	binary.Write(buf, order, ino.fileType)         // Type
	binary.Write(buf, order, uint16(ino.mode&0777)) // Perm (lower 9 bits)

	// Get UID/GID indices
	uidIdx := w.idTable[ino.uid]
	gidIdx := w.idTable[ino.gid]
	binary.Write(buf, order, uint16(uidIdx))
	binary.Write(buf, order, uint16(gidIdx))
	binary.Write(buf, order, int32(ino.modTime)) // ModTime
	binary.Write(buf, order, ino.ino)            // Ino

	// Type-specific data
	switch ino.fileType {
	case DirType: // Basic Directory
		binary.Write(buf, order, uint32(0))         // StartBlock (directory table offset, filled later)
		binary.Write(buf, order, ino.nlink)         // NLink
		binary.Write(buf, order, uint16(0))         // Size (directory size in bytes, filled later)
		binary.Write(buf, order, uint16(0))         // Offset (offset within directory table block)
		binary.Write(buf, order, uint32(1))         // ParentIno (root's parent is itself)
	case FileType: // Basic File
		binary.Write(buf, order, uint32(0))         // StartBlock
		binary.Write(buf, order, uint32(0xFFFFFFFF)) // FragBlock (no fragment)
		binary.Write(buf, order, uint32(0))         // FragOfft
		binary.Write(buf, order, uint32(ino.size))  // Size
		// No block list for empty files
	default:
		return nil, fmt.Errorf("unsupported inode type %d", ino.fileType)
	}

	return buf.Bytes(), nil
}

// writeInodeTable writes all inodes to the inode table
func (w *Writer) writeInodeTable() error {
	w.inodeTableStart = w.offset

	// Collect all inode data
	inodeBuf := &bytes.Buffer{}

	for _, ino := range w.inodes {
		data, err := w.serializeInode(ino)
		if err != nil {
			return err
		}
		inodeBuf.Write(data)
	}

	// Write as metadata block
	_, err := w.writeMetadataBlock(inodeBuf.Bytes())
	return err
}

// writeDirectoryTable writes directory entries
func (w *Writer) writeDirectoryTable() error {
	w.dirTableStart = w.offset

	// For minimal implementation, write an empty root directory
	// Directory format: header + entries
	// Header: count (uint32), start_block (uint32), inode_number (uint32)

	dirBuf := &bytes.Buffer{}
	order := binary.LittleEndian

	// Empty directory - no entries
	binary.Write(dirBuf, order, uint32(0)) // count = 0 (no entries)
	binary.Write(dirBuf, order, uint32(0)) // start_block
	binary.Write(dirBuf, order, uint32(1)) // inode_number (root)

	// Write as metadata block
	_, err := w.writeMetadataBlock(dirBuf.Bytes())
	return err
}

// Finalize writes the complete SquashFS filesystem to the underlying writer.
// After this method returns, the filesystem image is complete and the Writer
// should not be used again.
func (w *Writer) Finalize() error {
	// Build ID table
	if err := w.buildIDTable(); err != nil {
		return err
	}

	// TODO: Write data blocks (for regular files)
	// For now, skip data blocks - only support empty files and directories

	// Write directory table first (inodes reference it)
	if err := w.writeDirectoryTable(); err != nil {
		return err
	}

	// Write inode table
	if err := w.writeInodeTable(); err != nil {
		return err
	}

	// Write ID table
	if err := w.writeIDTable(); err != nil {
		return err
	}

	// TODO: Write fragment table (can be empty)
	// TODO: Write export table (can be empty)

	w.bytesUsed = w.offset

	// Build and write superblock
	sb := w.buildSuperblock()

	// Write superblock
	if w.wa != nil {
		// Update superblock at offset 0
		_, err := w.wa.WriteAt(sb, 0)
		return err
	}

	// For buffered mode, copy superblock to the beginning of buffer
	data := w.buf.Bytes()
	copy(data[0:SuperblockSize], sb)

	// Write everything to the final writer
	_, err := w.w.Write(data)
	return err
}

// buildSuperblock constructs the superblock structure
func (w *Writer) buildSuperblock() []byte {
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
	// Fragment count
	order.PutUint32(sb[16:20], 0) // no fragments yet
	// Compression
	order.PutUint16(sb[20:22], uint16(w.comp))
	// Block log
	blockLog := uint16(0)
	for i := uint16(0); i < 32; i++ {
		if (1 << i) == w.blockSize {
			blockLog = i
			break
		}
	}
	order.PutUint16(sb[22:24], blockLog)
	// Flags
	order.PutUint16(sb[24:26], uint16(w.flags))
	// ID count
	order.PutUint16(sb[26:28], uint16(len(w.idList)))
	// Version
	order.PutUint16(sb[28:30], 4) // major
	order.PutUint16(sb[30:32], 0) // minor
	// Root inode - reference to inode at offset 0 in inode table
	// inodeRef format: block offset in upper bits, offset within block in lower bits
	// For first inode, this is just 0
	order.PutUint64(sb[32:40], 0)
	// Bytes used
	order.PutUint64(sb[40:48], w.bytesUsed)
	// ID table start
	order.PutUint64(sb[48:56], w.idTableStart)
	// Xattr ID table start
	order.PutUint64(sb[56:64], 0xFFFFFFFFFFFFFFFF) // no xattrs
	// Inode table start
	order.PutUint64(sb[64:72], w.inodeTableStart)
	// Directory table start
	order.PutUint64(sb[72:80], w.dirTableStart)
	// Fragment table start
	order.PutUint64(sb[80:88], w.fragTableStart)
	// Export table start
	order.PutUint64(sb[88:96], w.exportTableStart)

	return sb
}
