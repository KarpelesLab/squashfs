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
	inodeMap   map[string]*writerInode // path -> inode mapping

	// Data tracking
	idTable map[uint32]uint32 // uid/gid -> index mapping
	idList  []uint32          // ordered list of uid/gid values

	// Source filesystem for reading file data
	srcFS fs.FS

	// Table positions (filled during Finalize)
	idTableStart     uint64
	inodeTableStart  uint64
	dirTableStart    uint64
	fragTableStart   uint64
	exportTableStart uint64
	bytesUsed        uint64
}

// writerInode represents an inode being built in memory
type writerInode struct {
	path string
	name string
	ino  uint32

	// File metadata
	mode     fs.FileMode
	size     uint64
	modTime  int64
	uid      uint32
	gid      uint32
	nlink    uint32
	fileType Type

	// For directories
	entries []*writerInode
	parent  *writerInode

	// Directory table info (filled during Finalize)
	dirOffset   uint32           // offset in directory table
	dirBlockRef uint64           // block reference for directory data
	dirIndex    []DirIndexEntry  // directory index for large directories

	// File data info (filled during Finalize)
	dataBlocks []uint32 // block sizes for file data
	startBlock uint64   // start position of file data

	// Inode table info (filled during writeInodeTable)
	inodeOffset uint32 // offset in inode table
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

// SetSourceFS sets the source filesystem to read file data from.
// This must be called before Finalize() if you want to write file contents.
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
	case info.Mode()&fs.ModeSymlink != 0:
		inode.fileType = SymlinkType
		// TODO: Read symlink target
	default:
		// TODO: Handle other file types (char, block, fifo, socket)
		inode.fileType = FileType // treat as regular file for now
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
	default:
		return nil, fmt.Errorf("unsupported inode type %d", ino.fileType)
	}

	return buf.Bytes(), nil
}

// writeInodeTable writes all inodes to the inode table
func (w *Writer) writeInodeTable() error {
	w.inodeTableStart = w.offset

	// Collect all inode data (offsets were already computed)
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

// writeDirectoryTable writes directory entries for all directories
func (w *Writer) writeDirectoryTable() error {
	w.dirTableStart = w.offset

	dirBuf := &bytes.Buffer{}
	order := binary.LittleEndian

	// Write directory data for all directory inodes
	for _, inode := range w.inodes {
		if inode.fileType != DirType {
			continue
		}

		// Remember where this directory starts in the buffer
		dirStart := dirBuf.Len()
		inode.dirOffset = uint32(dirStart) // offset within the uncompressed metadata block

		// Sort entries by name for consistency
		sortInodes(inode.entries)

		// Build directory index for large directories
		inode.dirIndex = make([]DirIndexEntry, 0)
		const indexInterval = 256 // Create index entry every 256 entries

		// Write directory entries
		if len(inode.entries) == 0 {
			// Empty directory - still need a minimal structure
			if err := writeBinary(dirBuf, order, uint32(0)); err != nil { // count = 0
				return err
			}
			if err := writeBinary(dirBuf, order, uint32(0)); err != nil { // start_block
				return err
			}
			if err := writeBinary(dirBuf, order, inode.ino); err != nil { // inode_number
				return err
			}
		} else {
			// For large directories, break into chunks and create index
			for chunkStart := 0; chunkStart < len(inode.entries); chunkStart += indexInterval {
				chunkEnd := chunkStart + indexInterval
				if chunkEnd > len(inode.entries) {
					chunkEnd = len(inode.entries)
				}
				chunk := inode.entries[chunkStart:chunkEnd]

				// Add index entry if this is a large directory
				if len(inode.entries) > indexInterval {
					indexPos := dirBuf.Len() - dirStart
					inode.dirIndex = append(inode.dirIndex, DirIndexEntry{
						Index: uint32(indexPos),
						Start: 0, // all in same block
						Name:  chunk[0].name,
					})
				}

				// Write header: count is (number of entries in chunk - 1)
				if err := writeBinary(dirBuf, order, uint32(len(chunk)-1)); err != nil {
					return err
				}
				// start_block - block offset in inode table where inodes start
				// For simplicity, use 0 since all inodes are in one block
				if err := writeBinary(dirBuf, order, uint32(0)); err != nil {
					return err
				}
				// inode_number - first inode number in this chunk
				if err := writeBinary(dirBuf, order, chunk[0].ino); err != nil {
					return err
				}

				// Write entries in this chunk
				for _, entry := range chunk {
					// offset - offset in inode table (now we know the actual offset)
					if err := writeBinary(dirBuf, order, uint16(entry.inodeOffset)); err != nil {
						return err
					}

					// inode_number difference from header inode
					inoDiff := int16(entry.ino) - int16(chunk[0].ino)
					if err := writeBinary(dirBuf, order, inoDiff); err != nil {
						return err
					}

					// type
					if err := writeBinary(dirBuf, order, entry.fileType); err != nil {
						return err
					}

					// size - length of name minus 1
					if err := writeBinary(dirBuf, order, uint16(len(entry.name)-1)); err != nil {
						return err
					}

					// name
					if err := writeBinary(dirBuf, order, []byte(entry.name)); err != nil {
						return err
					}
				}
			}
		}

		// Store the size for the inode
		inode.size = uint64(dirBuf.Len() - dirStart)

		// Use Extended Directory type if directory has index
		if len(inode.dirIndex) > 0 {
			inode.fileType = XDirType
		}
	}

	// Write all directory data as metadata blocks
	if dirBuf.Len() > 0 {
		_, err := w.writeMetadataBlock(dirBuf.Bytes())
		return err
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
		if w.srcFS == nil {
			// No source FS, write empty file
			continue
		}

		data, err := fs.ReadFile(w.srcFS, inode.path)
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

// computeInodeOffsets pre-computes inode offsets in the inode table
func (w *Writer) computeInodeOffsets() error {
	offset := uint32(0)
	for _, ino := range w.inodes {
		ino.inodeOffset = offset
		data, err := w.serializeInode(ino)
		if err != nil {
			return err
		}
		offset += uint32(len(data))
	}
	return nil
}

// Finalize writes the complete SquashFS filesystem to the underlying writer.
// After this method returns, the filesystem image is complete and the Writer
// should not be used again.
func (w *Writer) Finalize() error {
	// Build ID table
	if err := w.buildIDTable(); err != nil {
		return err
	}

	// Write data blocks for regular files
	if err := w.writeFileData(); err != nil {
		return err
	}

	// Pre-compute inode offsets before writing directory table
	if err := w.computeInodeOffsets(); err != nil {
		return err
	}

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
