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
//
// The Writer maintains an in-memory representation of the filesystem tree,
// including all inodes, directory structures, and file metadata. When Finalize()
// is called, it performs the following steps:
//  1. Writes file data blocks
//  2. Computes directory structures and indices
//  3. Builds and writes the inode table
//  4. Writes the directory table
//  5. Writes the ID (UID/GID) table
//  6. Updates the superblock with final offsets
type Writer struct {
	w      io.Writer
	wa     io.WriterAt   // set if w implements WriterAt
	buf    *bytes.Buffer // used when w doesn't implement WriterAt
	offset uint64        // current write offset

	// Filesystem metadata
	blockSize uint32
	comp      Compression
	modTime   int32
	flags     Flags

	// In-memory inode tree
	inodes     []*writerInode
	rootInode  *writerInode
	inodeCount uint32
	inodeMap   map[string]*writerInode // path -> inode mapping

	// Data tracking
	idTable map[uint32]uint32 // uid/gid -> index mapping
	idList  []uint32          // ordered list of uid/gid values

	// Default source filesystem (captured by Add() into each inode)
	srcFS fs.FS

	// Table positions (filled during Finalize)
	idTableStart     uint64
	inodeTableStart  uint64
	dirTableStart    uint64
	fragTableStart   uint64
	exportTableStart uint64
	bytesUsed        uint64

	// Pre-compressed directory blocks (computed during inode table building)
	precompressedDirBlocks [][]byte

	// Superblock instance (populated during Finalize)
	sb Superblock
}

// writerInode represents an inode being built in memory.
// Each inode corresponds to a file, directory, symlink, or special file
// in the filesystem. The inode contains metadata and references to the
// actual data (for files) or directory entries (for directories).
type writerInode struct {
	path string
	name string
	ino  uint32

	// File metadata
	mode      fs.FileMode
	size      uint64
	modTime   int64
	uid       uint32
	gid       uint32
	nlink     uint32
	fileType  Type
	symTarget string // symlink target path

	// Source filesystem for reading file data
	srcFS fs.FS

	// For directories
	entries []*writerInode
	parent  *writerInode

	// Directory table info (computed during inode table building)
	dirOffset uint32          // offset in directory table
	dirIndex  []DirIndexEntry // directory index for large directories (XDirType only)
	dirData   []byte          // serialized directory data

	// File data info (filled during writeFileData)
	dataBlocks []uint32 // block sizes for file data (with compression flag in high bit)
	startBlock uint64   // start position of file data in the image

	// Inode table info (computed during inode position calculation)
	inodeBlockStart uint32 // byte offset from inode table start to this inode's metadata block
	inodeOffset     uint32 // offset within the metadata block
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
		inodeMap:  make(map[string]*writerInode),
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

// SetSourceFS sets the default source filesystem to read file data from.
// This filesystem will be used for subsequent Add() calls.
// You can call SetSourceFS() multiple times to add files from different filesystems.
func (w *Writer) SetSourceFS(srcFS fs.FS) {
	w.srcFS = srcFS
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
		w.inodeMap["."] = w.rootInode
		w.inodeMap[""] = w.rootInode
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
		srcFS:   w.srcFS, // Capture current source filesystem
	}

	// Extract uid/gid from info.Sys() if available
	if sys := info.Sys(); sys != nil {
		if statT, ok := sys.(interface {
			Uid() uint32
			Gid() uint32
		}); ok {
			inode.uid = statT.Uid()
			inode.gid = statT.Gid()
		}
	}

	// Determine inode type
	switch {
	case info.Mode().IsDir():
		inode.fileType = DirType
		inode.entries = make([]*writerInode, 0)
		inode.nlink = 2
	case info.Mode().IsRegular():
		inode.fileType = FileType
	case info.Mode()&fs.ModeSymlink != 0:
		inode.fileType = SymlinkType
		// Read symlink target
		if inode.srcFS != nil {
			target, err := fs.ReadLink(inode.srcFS, path)
			if err != nil {
				return fmt.Errorf("failed to read symlink %s: %w", path, err)
			}
			inode.symTarget = target
			inode.size = uint64(len(target))
		}
	case info.Mode()&fs.ModeCharDevice != 0:
		inode.fileType = CharDevType
	case info.Mode()&fs.ModeDevice != 0:
		inode.fileType = BlockDevType
	case info.Mode()&fs.ModeNamedPipe != 0:
		inode.fileType = FifoType
	case info.Mode()&fs.ModeSocket != 0:
		inode.fileType = SocketType
	default:
		// Unknown type, treat as regular file
		inode.fileType = FileType
	}

	// Add to inode list and map
	w.inodes = append(w.inodes, inode)
	w.inodeMap[path] = inode

	// Build directory tree structure
	parentPath := getParentPath(path)
	parent := w.inodeMap[parentPath]
	if parent == nil {
		// Parent doesn't exist, shouldn't happen with WalkDir
		return fmt.Errorf("parent directory not found for %s", path)
	}

	inode.parent = parent
	parent.entries = append(parent.entries, inode)

	return nil
}

// getParentPath returns the parent directory path
func getParentPath(path string) string {
	if path == "" || path == "." {
		return ""
	}
	// Find last slash
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "."
			}
			return path[:i]
		}
	}
	return "."
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

// writeBinary is a helper that writes to a buffer and checks for errors
func writeBinary(buf *bytes.Buffer, order binary.ByteOrder, data interface{}) error {
	return binary.Write(buf, order, data)
}

// serializeInode serializes an inode to bytes (Basic Directory type only for now)
func (w *Writer) serializeInode(ino *writerInode) ([]byte, error) {
	buf := &bytes.Buffer{}
	order := binary.LittleEndian

	// Common inode header
	if err := writeBinary(buf, order, ino.fileType); err != nil {
		return nil, err
	}
	if err := writeBinary(buf, order, uint16(ino.mode&0777)); err != nil {
		return nil, err
	}

	// Get UID/GID indices
	uidIdx := w.idTable[ino.uid]
	gidIdx := w.idTable[ino.gid]
	if err := writeBinary(buf, order, uint16(uidIdx)); err != nil {
		return nil, err
	}
	if err := writeBinary(buf, order, uint16(gidIdx)); err != nil {
		return nil, err
	}
	if err := writeBinary(buf, order, int32(ino.modTime)); err != nil {
		return nil, err
	}
	if err := writeBinary(buf, order, ino.ino); err != nil {
		return nil, err
	}

	// Type-specific data
	switch ino.fileType {
	case DirType: // Basic Directory
		// start_block - block offset from directory table start (0 for first block)
		if err := writeBinary(buf, order, uint32(0)); err != nil {
			return nil, err
		}
		// nlink
		if err := writeBinary(buf, order, ino.nlink); err != nil {
			return nil, err
		}
		// file_size - directory size
		if err := writeBinary(buf, order, uint16(ino.size)); err != nil {
			return nil, err
		}
		// offset - offset within the uncompressed block
		if err := writeBinary(buf, order, uint16(ino.dirOffset)); err != nil {
			return nil, err
		}
		// parent_inode - inode number of parent directory
		parentIno := uint32(1) // root by default
		if ino.parent != nil {
			parentIno = ino.parent.ino
		}
		if err := writeBinary(buf, order, parentIno); err != nil {
			return nil, err
		}
	case XDirType: // Extended Directory with index
		// nlink
		if err := writeBinary(buf, order, ino.nlink); err != nil {
			return nil, err
		}
		// file_size - directory size (32-bit)
		if err := writeBinary(buf, order, uint32(ino.size)); err != nil {
			return nil, err
		}
		// start_block - block offset from directory table start
		if err := writeBinary(buf, order, uint32(0)); err != nil {
			return nil, err
		}
		// parent_inode - inode number of parent directory
		parentIno := uint32(1) // root by default
		if ino.parent != nil {
			parentIno = ino.parent.ino
		}
		if err := writeBinary(buf, order, parentIno); err != nil {
			return nil, err
		}
		// index_count - number of index entries
		if err := writeBinary(buf, order, uint16(len(ino.dirIndex))); err != nil {
			return nil, err
		}
		// offset - offset within the uncompressed block
		if err := writeBinary(buf, order, uint16(ino.dirOffset)); err != nil {
			return nil, err
		}
		// xattr_idx
		if err := writeBinary(buf, order, uint32(0xFFFFFFFF)); err != nil {
			return nil, err
		}
		// directory index entries
		for _, idx := range ino.dirIndex {
			// index - position in directory listing
			if err := writeBinary(buf, order, idx.Index); err != nil {
				return nil, err
			}
			// start - directory table block offset
			if err := writeBinary(buf, order, idx.Start); err != nil {
				return nil, err
			}
			// size - length of name minus 1
			if err := writeBinary(buf, order, uint32(len(idx.Name)-1)); err != nil {
				return nil, err
			}
			// name
			if err := writeBinary(buf, order, []byte(idx.Name)); err != nil {
				return nil, err
			}
		}
	case FileType: // Basic File
		// start_block - absolute position of first data block
		if err := writeBinary(buf, order, uint32(ino.startBlock)); err != nil {
			return nil, err
		}
		// fragment - fragment index (0xFFFFFFFF = no fragment)
		if err := writeBinary(buf, order, uint32(0xFFFFFFFF)); err != nil {
			return nil, err
		}
		// offset - offset within fragment (unused if no fragment)
		if err := writeBinary(buf, order, uint32(0)); err != nil {
			return nil, err
		}
		// file_size
		if err := writeBinary(buf, order, uint32(ino.size)); err != nil {
			return nil, err
		}
		// block_list - array of block sizes
		for _, blockSize := range ino.dataBlocks {
			if err := writeBinary(buf, order, blockSize); err != nil {
				return nil, err
			}
		}
	case SymlinkType: // Basic Symlink
		// nlink
		if err := writeBinary(buf, order, ino.nlink); err != nil {
			return nil, err
		}
		// symlink_size - length of target path
		if err := writeBinary(buf, order, uint32(len(ino.symTarget))); err != nil {
			return nil, err
		}
		// symlink - target path
		if err := writeBinary(buf, order, []byte(ino.symTarget)); err != nil {
			return nil, err
		}
	case CharDevType, BlockDevType: // Device nodes
		// nlink
		if err := writeBinary(buf, order, ino.nlink); err != nil {
			return nil, err
		}
		// rdev - device number (major/minor)
		// For now, write 0 as we don't extract device numbers from source
		if err := writeBinary(buf, order, uint32(0)); err != nil {
			return nil, err
		}
	case FifoType, SocketType: // Named pipes and sockets
		// nlink
		if err := writeBinary(buf, order, ino.nlink); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported inode type %d", ino.fileType)
	}

	return buf.Bytes(), nil
}

const (
	maxMetadataBlockSize = 8192 // SquashFS metadata block size
	indexInterval        = 256  // Directory index interval
)

// inodePosition tracks where an inode is located in the metadata blocks
type inodePosition struct {
	blockNum int    // which metadata block (0, 1, 2, ...)
	offset   uint32 // offset within that block
}

// buildDirectoryEntryData builds directory entry data for an inode
func (w *Writer) buildDirectoryEntryData(inode *writerInode, inodePos map[uint32]inodePosition, blockPositions []uint32) ([]byte, error) {
	if inode.fileType != DirType && inode.fileType != XDirType {
		return nil, nil
	}

	dirBuf := &bytes.Buffer{}
	order := binary.LittleEndian

	if len(inode.entries) == 0 {
		// Empty directory
		if err := writeBinary(dirBuf, order, uint32(0)); err != nil {
			return nil, err
		}
		if err := writeBinary(dirBuf, order, uint32(0)); err != nil {
			return nil, err
		}
		if err := writeBinary(dirBuf, order, inode.ino); err != nil {
			return nil, err
		}
		return dirBuf.Bytes(), nil
	}

	// Reset directory index for XDirType
	if inode.fileType == XDirType {
		inode.dirIndex = make([]DirIndexEntry, 0)
	}

	// Build chunks
	entryIdx := 0
	for entryIdx < len(inode.entries) {
		chunkStart := entryIdx
		firstEntryBlock := inodePos[inode.entries[chunkStart].ino].blockNum

		// Find end of chunk: stop at block boundary or 256 entries
		chunkEnd := chunkStart
		for chunkEnd < len(inode.entries) &&
			(chunkEnd-chunkStart) < indexInterval &&
			inodePos[inode.entries[chunkEnd].ino].blockNum == firstEntryBlock {
			chunkEnd++
		}

		chunkEntries := inode.entries[chunkStart:chunkEnd]

		// Add directory index entry for XDirType
		if inode.fileType == XDirType {
			inode.dirIndex = append(inode.dirIndex, DirIndexEntry{
				Index: uint32(dirBuf.Len()),
				Start: 0, // Will be set in computeDirectoryTableOffsets
				Name:  chunkEntries[0].name,
			})
		}

		// Write chunk header
		if err := writeBinary(dirBuf, order, uint32(len(chunkEntries)-1)); err != nil {
			return nil, err
		}

		// Write block position (from blockPositions if available)
		blockPos := uint32(0)
		if blockPositions != nil && firstEntryBlock < len(blockPositions) {
			blockPos = blockPositions[firstEntryBlock]
		}
		if err := writeBinary(dirBuf, order, blockPos); err != nil {
			return nil, err
		}

		if err := writeBinary(dirBuf, order, chunkEntries[0].ino); err != nil {
			return nil, err
		}

		// Write entries
		for _, entry := range chunkEntries {
			if err := writeBinary(dirBuf, order, uint16(inodePos[entry.ino].offset)); err != nil {
				return nil, err
			}
			if err := writeBinary(dirBuf, order, int16(entry.ino)-int16(chunkEntries[0].ino)); err != nil {
				return nil, err
			}
			if err := writeBinary(dirBuf, order, entry.fileType); err != nil {
				return nil, err
			}
			if err := writeBinary(dirBuf, order, uint16(len(entry.name)-1)); err != nil {
				return nil, err
			}
			if err := writeBinary(dirBuf, order, []byte(entry.name)); err != nil {
				return nil, err
			}
		}

		entryIdx = chunkEnd
	}

	return dirBuf.Bytes(), nil
}

// computeInodePositions determines which metadata block each inode will be in
// Returns a map of inode number to position (block number and offset within block)
func (w *Writer) computeInodePositions() (map[uint32]inodePosition, error) {
	inodePos := make(map[uint32]inodePosition)
	currentBlock := 0
	blockBuf := &bytes.Buffer{}

	for _, ino := range w.inodes {
		data, err := w.serializeInode(ino)
		if err != nil {
			return nil, err
		}

		// Start new block if current one would overflow
		if blockBuf.Len() > 0 && blockBuf.Len()+len(data) > maxMetadataBlockSize {
			currentBlock++
			blockBuf.Reset()
		}

		inodePos[ino.ino] = inodePosition{
			blockNum: currentBlock,
			offset:   uint32(blockBuf.Len()),
		}

		blockBuf.Write(data)
	}

	return inodePos, nil
}

// computeBlockPositions calculates the byte offsets of each metadata block after compression
func (w *Writer) computeBlockPositions() ([]uint32, error) {
	tempBuf := &bytes.Buffer{}
	blockBuf := &bytes.Buffer{}
	blockPositions := []uint32{0}

	for _, ino := range w.inodes {
		data, err := w.serializeInode(ino)
		if err != nil {
			return nil, err
		}

		if blockBuf.Len() > 0 && blockBuf.Len()+len(data) > maxMetadataBlockSize {
			blockData := blockBuf.Bytes()
			compressed, _ := w.comp.compress(blockData)

			var blockSize int
			if compressed != nil && len(compressed) < len(blockData) {
				blockSize = 2 + len(compressed)
			} else {
				blockSize = 2 + len(blockData)
			}

			tempBuf.Write(make([]byte, blockSize))
			blockPositions = append(blockPositions, uint32(tempBuf.Len()))
			blockBuf.Reset()
		}

		blockBuf.Write(data)
	}

	return blockPositions, nil
}

// serializeInodesToBuffer writes all inodes as compressed metadata blocks
func (w *Writer) serializeInodesToBuffer() ([]byte, error) {
	result := &bytes.Buffer{}
	blockBuf := &bytes.Buffer{}

	for _, ino := range w.inodes {
		data, err := w.serializeInode(ino)
		if err != nil {
			return nil, err
		}

		if blockBuf.Len() > 0 && blockBuf.Len()+len(data) > maxMetadataBlockSize {
			if err := w.writeCompressedMetadataBlock(result, blockBuf.Bytes()); err != nil {
				return nil, err
			}
			blockBuf.Reset()
		}

		blockBuf.Write(data)
	}

	// Write final block
	if blockBuf.Len() > 0 {
		if err := w.writeCompressedMetadataBlock(result, blockBuf.Bytes()); err != nil {
			return nil, err
		}
	}

	return result.Bytes(), nil
}

// writeCompressedMetadataBlock compresses and writes a metadata block to the buffer
func (w *Writer) writeCompressedMetadataBlock(buf *bytes.Buffer, blockData []byte) error {
	compressed, _ := w.comp.compress(blockData)

	header := make([]byte, 2)
	if compressed != nil && len(compressed) < len(blockData) {
		// Write compressed
		binary.LittleEndian.PutUint16(header, uint16(len(compressed)))
		buf.Write(header)
		buf.Write(compressed)
	} else {
		// Write uncompressed
		binary.LittleEndian.PutUint16(header, uint16(len(blockData))|0x8000)
		buf.Write(header)
		buf.Write(blockData)
	}

	return nil
}

// simulateDirectoryIndices simulates building directory data to compute Index values for XDirType
func (w *Writer) simulateDirectoryIndices(inodePos map[uint32]inodePosition) error {
	order := binary.LittleEndian

	for _, inode := range w.inodes {
		if inode.fileType != XDirType || len(inodePos) == 0 {
			continue
		}

		dirBuf := &bytes.Buffer{}
		inode.dirIndex = make([]DirIndexEntry, 0)

		entryIdx := 0
		for entryIdx < len(inode.entries) {
			chunkStart := entryIdx
			firstEntryBlock := inodePos[inode.entries[chunkStart].ino].blockNum

			chunkEnd := chunkStart
			for chunkEnd < len(inode.entries) &&
				(chunkEnd-chunkStart) < indexInterval &&
				inodePos[inode.entries[chunkEnd].ino].blockNum == firstEntryBlock {
				chunkEnd++
			}

			chunk := inode.entries[chunkStart:chunkEnd]

			inode.dirIndex = append(inode.dirIndex, DirIndexEntry{
				Index: uint32(dirBuf.Len()),
				Start: 0,
				Name:  chunk[0].name,
			})

			// Simulate writing the chunk to advance the position
			if err := writeBinary(dirBuf, order, uint32(len(chunk)-1)); err != nil {
				return err
			}
			if err := writeBinary(dirBuf, order, uint32(0)); err != nil {
				return err
			}
			if err := writeBinary(dirBuf, order, chunk[0].ino); err != nil {
				return err
			}
			for _, entry := range chunk {
				if err := writeBinary(dirBuf, order, uint16(0)); err != nil {
					return err
				}
				if err := writeBinary(dirBuf, order, int16(entry.ino)-int16(chunk[0].ino)); err != nil {
					return err
				}
				if err := writeBinary(dirBuf, order, entry.fileType); err != nil {
					return err
				}
				if err := writeBinary(dirBuf, order, uint16(len(entry.name)-1)); err != nil {
					return err
				}
				if err := writeBinary(dirBuf, order, []byte(entry.name)); err != nil {
					return err
				}
			}

			entryIdx = chunkEnd
		}
	}

	return nil
}

// inodePositionsEqual checks if two inode position maps are equal
func (w *Writer) inodePositionsEqual(a, b map[uint32]inodePosition) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// buildDirectoryDataForAllInodes builds directory data for all directory inodes
func (w *Writer) buildDirectoryDataForAllInodes(inodePos map[uint32]inodePosition, blockPositions []uint32) error {
	globalDirOffset := uint32(0)

	for _, inode := range w.inodes {
		if inode.fileType != DirType && inode.fileType != XDirType {
			continue
		}

		inode.dirOffset = globalDirOffset
		dirData, err := w.buildDirectoryEntryData(inode, inodePos, blockPositions)
		if err != nil {
			return err
		}

		inode.dirData = dirData
		inode.size = uint64(len(dirData))
		globalDirOffset += uint32(len(dirData))
	}

	return nil
}

// blockPositionsEqual checks if two block position slices are equal
func (w *Writer) blockPositionsEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rebuildDirectoryDataWithBlockPositions rebuilds directory data with updated block positions
// and validates that directory sizes remain unchanged
func (w *Writer) rebuildDirectoryDataWithBlockPositions(inodePos map[uint32]inodePosition, blockPositions []uint32) error {
	globalDirOffset := uint32(0)

	for _, inode := range w.inodes {
		if inode.fileType != DirType && inode.fileType != XDirType {
			continue
		}

		oldSize := inode.size
		inode.dirOffset = globalDirOffset

		dirData, err := w.buildDirectoryEntryData(inode, inodePos, blockPositions)
		if err != nil {
			return err
		}

		inode.dirData = dirData
		newSize := uint64(len(dirData))
		inode.size = newSize

		// Validate size hasn't changed
		if oldSize != 0 && oldSize != newSize {
			return fmt.Errorf("directory size changed from %d to %d for inode %d", oldSize, newSize, inode.ino)
		}

		globalDirOffset += uint32(len(dirData))
	}

	return nil
}

// buildInodeTableToBuffer builds the complete inode table in a buffer,
// computing and recording inode positions and directory offsets.
//
// This function performs multiple passes:
// 1. Compute inode positions (which metadata block each inode is in)
// 2. Build initial directory data
// 3. Iteratively compute block positions and rebuild directory data until convergence
// 4. Serialize final inodes to buffer
func (w *Writer) buildInodeTableToBuffer() ([]byte, error) {
	// PASS 1: Iteratively determine inode positions
	// (Iteration needed because dirIndex size depends on chunk boundaries)
	var inodePos map[uint32]inodePosition

	// Clear directory data temporarily
	for _, ino := range w.inodes {
		if ino.fileType == DirType || ino.fileType == XDirType {
			ino.size = 0
			ino.dirOffset = 0
			if ino.fileType == XDirType {
				ino.dirIndex = nil
			}
		}
	}

	// Iterate until inode positions stabilize
	maxIterations := 10
	for iteration := 0; iteration < maxIterations; iteration++ {
		prevInodePos := make(map[uint32]inodePosition)
		for k, v := range inodePos {
			prevInodePos[k] = v
		}

		// Pre-allocate dirIndex entries for XDirType based on current inode positions
		if err := w.simulateDirectoryIndices(inodePos); err != nil {
			return nil, err
		}

		// Compute inode positions
		var err error
		inodePos, err = w.computeInodePositions()
		if err != nil {
			return nil, err
		}

		// Check if positions have stabilized
		if iteration > 0 && w.inodePositionsEqual(prevInodePos, inodePos) {
			break
		}

		if iteration == maxIterations-1 {
			return nil, fmt.Errorf("failed to converge inode positions after %d iterations", maxIterations)
		}
	}

	// PASS 2: Build initial directory data using inode block numbers (without block positions)
	if err := w.buildDirectoryDataForAllInodes(inodePos, nil); err != nil {
		return nil, err
	}

	// PASS 3+4: Iterate until blockPositions converges
	// Because compression may be non-deterministic, we need to rebuild directory data
	// and recalculate blockPositions until they stabilize
	var blockPositions []uint32
	maxDirIterations := 10

	for dirIter := 0; dirIter < maxDirIterations; dirIter++ {
		// Compute directory table offsets for DirIndexEntry.Start fields
		// (Must be done before computing block positions so Start values are correct)
		if err := w.computeDirectoryTableOffsets(); err != nil {
			return nil, err
		}

		// PASS 3: Calculate block positions after compression
		newBlockPositions, err := w.computeBlockPositions()
		if err != nil {
			return nil, err
		}

		// Check if converged
		if dirIter > 0 && w.blockPositionsEqual(blockPositions, newBlockPositions) {
			blockPositions = newBlockPositions
			break
		}

		blockPositions = newBlockPositions

		if dirIter == maxDirIterations-1 {
			return nil, fmt.Errorf("blockPositions failed to converge after %d iterations", maxDirIterations)
		}

		// PASS 4: Rebuild directory data with new block positions
		if err := w.rebuildDirectoryDataWithBlockPositions(inodePos, blockPositions); err != nil {
			return nil, err
		}
	}

	// PASS 5: Serialize inodes with final directory data and write to output
	result, err := w.serializeInodesToBuffer()
	if err != nil {
		return nil, err
	}

	// Set final inode positions based on block positions
	for _, ino := range w.inodes {
		ino.inodeBlockStart = blockPositions[inodePos[ino.ino].blockNum]
		ino.inodeOffset = inodePos[ino.ino].offset
	}

	return result, nil
}

// computeDirectoryTableOffsets pre-compresses directory blocks and updates Start fields
func (w *Writer) computeDirectoryTableOffsets() error {
	// Collect all directory data and track where each inode's data starts
	dirBuf := &bytes.Buffer{}
	inodeOffsets := make(map[uint32]uint32)

	for _, inode := range w.inodes {
		if inode.fileType != DirType && inode.fileType != XDirType {
			continue
		}
		inodeOffsets[inode.ino] = uint32(dirBuf.Len())
		dirBuf.Write(inode.dirData)
	}

	// Pre-compress and save blocks, tracking offsets
	data := dirBuf.Bytes()
	w.precompressedDirBlocks = make([][]byte, 0)
	blockOffsets := make(map[int]uint32)
	blockIdx := 0
	offset := uint32(0)

	for len(data) > 0 {
		blockSize := len(data)
		if blockSize > maxMetadataBlockSize {
			blockSize = maxMetadataBlockSize
		}

		blockOffsets[blockIdx] = offset

		// Compress and save the block
		blockData := data[:blockSize]
		compressed, _ := w.comp.compress(blockData)

		var toWrite []byte
		if compressed != nil && len(compressed) < blockSize {
			header := make([]byte, 2)
			binary.LittleEndian.PutUint16(header, uint16(len(compressed)))
			toWrite = append(header, compressed...)
		} else {
			header := make([]byte, 2)
			binary.LittleEndian.PutUint16(header, uint16(blockSize)|0x8000)
			toWrite = append(header, blockData...)
		}

		w.precompressedDirBlocks = append(w.precompressedDirBlocks, toWrite)
		offset += uint32(len(toWrite))
		data = data[blockSize:]
		blockIdx++
	}

	// Update DirIndexEntry.Start fields
	for _, inode := range w.inodes {
		if inode.fileType != XDirType || len(inode.dirIndex) == 0 {
			continue
		}

		inodeStart := inodeOffsets[inode.ino]
		for i := range inode.dirIndex {
			entryOffset := inodeStart + inode.dirIndex[i].Index
			blockNum := int(entryOffset / maxMetadataBlockSize)
			inode.dirIndex[i].Start = blockOffsets[blockNum]
		}
	}

	return nil
}

// writeDirectoryTable writes the pre-compressed directory blocks to disk
func (w *Writer) writeDirectoryTable() error {
	w.dirTableStart = w.offset

	// Write the pre-compressed blocks
	for _, block := range w.precompressedDirBlocks {
		if err := w.write(block); err != nil {
			return err
		}
	}

	return nil
}

// sortInodes sorts inodes by name
func sortInodes(inodes []*writerInode) {
	// Simple bubble sort for now
	for i := 0; i < len(inodes); i++ {
		for j := i + 1; j < len(inodes); j++ {
			if inodes[i].name > inodes[j].name {
				inodes[i], inodes[j] = inodes[j], inodes[i]
			}
		}
	}
}

// writeFileData writes data blocks for all regular files
func (w *Writer) writeFileData() error {
	for _, inode := range w.inodes {
		if inode.fileType != FileType || inode.size == 0 {
			continue
		}

		// Read file data from source filesystem
		if inode.srcFS == nil {
			// No source FS, write empty file
			continue
		}

		data, err := fs.ReadFile(inode.srcFS, inode.path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", inode.path, err)
		}

		// Store the start block position
		inode.startBlock = w.offset

		// Write data in blocks
		blockSize := int(w.blockSize)
		inode.dataBlocks = make([]uint32, 0)

		for offset := 0; offset < len(data); offset += blockSize {
			end := offset + blockSize
			if end > len(data) {
				end = len(data)
			}
			block := data[offset:end]

			// Try to compress the block
			compressed, err := w.comp.compress(block)
			if err != nil || len(compressed) >= len(block) {
				// Write uncompressed
				if err := w.write(block); err != nil {
					return err
				}
				// Mark as uncompressed by setting high bit
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(block))|0x01000000)
			} else {
				// Write compressed
				if err := w.write(compressed); err != nil {
					return err
				}
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(compressed)))
			}
		}
	}
	return nil
}

// prepareDirectories prepares directory structures and determines inode types
func (w *Writer) prepareDirectories() error {
	const indexInterval = 256

	for _, inode := range w.inodes {
		if inode.fileType != DirType {
			continue
		}

		// Sort entries by name
		sortInodes(inode.entries)

		// Check if this directory needs an index (more than 256 entries)
		if len(inode.entries) > indexInterval {
			// This directory needs indexing, use XDirType
			inode.fileType = XDirType
			// Note: dirIndex will be built in writeDirectoryTable()
			// after we know the actual chunk boundaries based on inode blocks
		}
	}
	return nil
}

// Finalize writes the complete SquashFS filesystem to the underlying writer.
// After this method returns, the filesystem image is complete and the Writer
// should not be used again.
//
// The finalization process follows this order:
//  1. Write placeholder superblock (will be updated at the end)
//  2. Build UID/GID table
//  3. Write all file data blocks (compressed)
//  4. Prepare directory structures (determine DirType vs XDirType)
//  5. Build inode table with directory data (complex multi-pass process)
//  6. Write directory table (pre-compressed blocks)
//  7. Write inode table
//  8. Write ID table
//  9. Update superblock with final table offsets
func (w *Writer) Finalize() error {
	// Write placeholder superblock first (we'll update it at the end)
	placeholder := make([]byte, SuperblockSize)
	if err := w.write(placeholder); err != nil {
		return err
	}

	// Build ID table
	if err := w.buildIDTable(); err != nil {
		return err
	}

	// Write data blocks for regular files
	if err := w.writeFileData(); err != nil {
		return err
	}

	// Prepare directory structures (determines XDirType vs DirType)
	if err := w.prepareDirectories(); err != nil {
		return err
	}

	// Build inode table in a buffer (this also computes Start fields for DirIndexEntry)
	inodeTableData, err := w.buildInodeTableToBuffer()
	if err != nil {
		return err
	}

	// Write directory table
	if err := w.writeDirectoryTable(); err != nil {
		return err
	}

	// Write the pre-built inode table to disk
	w.inodeTableStart = w.offset
	if err := w.write(inodeTableData); err != nil {
		return err
	}

	// Write ID table
	if err := w.writeIDTable(); err != nil {
		return err
	}

	// Write fragment table (empty for now - no fragment support yet)
	w.fragTableStart = 0xFFFFFFFFFFFFFFFF // No fragments

	// Write export table (empty for now - not required for basic functionality)
	w.exportTableStart = 0xFFFFFFFFFFFFFFFF // No export table

	w.bytesUsed = w.offset

	// Build and write superblock
	w.buildSuperblock()
	sbData := w.sb.Bytes()

	// Write superblock
	if w.wa != nil {
		// Update superblock at offset 0
		_, err := w.wa.WriteAt(sbData, 0)
		return err
	}

	// For buffered mode, copy superblock to the beginning of buffer
	data := w.buf.Bytes()
	copy(data[0:SuperblockSize], sbData)

	// Write everything to the final writer
	_, err = w.w.Write(data)
	return err
}

// buildSuperblock constructs the superblock structure
func (w *Writer) buildSuperblock() {
	// Calculate block log
	blockLog := uint16(0)
	for i := uint16(0); i < 32; i++ {
		if (1 << i) == w.blockSize {
			blockLog = i
			break
		}
	}

	// Populate superblock fields
	w.sb.Magic = 0x73717368
	w.sb.InodeCnt = w.inodeCount
	w.sb.ModTime = w.modTime
	w.sb.BlockSize = w.blockSize
	w.sb.FragCount = 0 // no fragments yet
	w.sb.Comp = w.comp
	w.sb.BlockLog = blockLog
	w.sb.Flags = w.flags
	w.sb.IdCount = uint16(len(w.idList))
	w.sb.VMajor = 4
	w.sb.VMinor = 0
	w.sb.RootInode = 0 // reference to inode at offset 0 in inode table
	w.sb.BytesUsed = w.bytesUsed
	w.sb.IdTableStart = w.idTableStart
	w.sb.XattrIdTableStart = 0xFFFFFFFFFFFFFFFF // no xattrs
	w.sb.InodeTableStart = w.inodeTableStart
	w.sb.DirTableStart = w.dirTableStart
	w.sb.FragTableStart = w.fragTableStart
	w.sb.ExportTableStart = w.exportTableStart
	w.sb.order = binary.LittleEndian
}
