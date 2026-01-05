package squashfs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"
)

// Writer creates SquashFS filesystem images.
type Writer struct {
	w      io.Writer
	wa     io.WriterAt
	buf    *bytes.Buffer
	offset uint64

	// Configuration
	blockSize uint32
	comp      Compression
	modTime   int32
	flags     Flags

	// In-memory filesystem tree
	rootInode  *writerInode
	inodeCount uint32
	inodeMap   map[string]*writerInode

	// Data tracking
	idTable map[uint32]uint32
	idList  []uint32
	srcFS   fs.FS

	// Table positions (filled during Finalize)
	idTableStart     uint64
	inodeTableStart  uint64
	dirTableStart    uint64
	fragTableStart   uint64
	exportTableStart uint64
	bytesUsed        uint64

	sb Superblock
}

// writerInode represents an inode in the tree
type writerInode struct {
	path string
	name string
	ino  uint32

	mode      fs.FileMode
	size      uint64
	modTime   int64
	uid       uint32
	gid       uint32
	nlink     uint32
	fileType  Type
	symTarget string

	srcFS fs.FS

	entries []*writerInode
	parent  *writerInode

	// Physical inode position (set when inode is written to streaming meta)
	inodeBlockIdx uint32
	inodeOffset   uint16

	// Directory position in dir meta (set when directory entries are written)
	dirBlockIdx uint32
	dirOffset   uint16

	// File data info
	dataBlocks []uint32
	startBlock uint64
}

// WriterOption configures a Writer
type WriterOption func(*Writer) error

func WithBlockSize(size uint32) WriterOption {
	return func(w *Writer) error {
		w.blockSize = size
		return nil
	}
}

func WithCompression(comp Compression) WriterOption {
	return func(w *Writer) error {
		w.comp = comp
		return nil
	}
}

func WithModTime(t time.Time) WriterOption {
	return func(w *Writer) error {
		w.modTime = int32(t.Unix())
		return nil
	}
}

func NewWriter(w io.Writer, opts ...WriterOption) (*Writer, error) {
	writer := &Writer{
		w:         w,
		blockSize: 131072, // 128KB default
		comp:      GZip,
		modTime:   int32(time.Now().Unix()),
		idTable:   make(map[uint32]uint32),
		inodeMap:  make(map[string]*writerInode),
	}

	if wa, ok := w.(io.WriterAt); ok {
		writer.wa = wa
		writer.offset = SuperblockSize
	} else {
		writer.buf = &bytes.Buffer{}
		writer.buf.Write(make([]byte, SuperblockSize))
		writer.offset = SuperblockSize
	}

	writer.rootInode = &writerInode{
		ino:      1,
		mode:     fs.ModeDir | 0755,
		modTime:  time.Now().Unix(),
		nlink:    2,
		fileType: DirType,
		entries:  make([]*writerInode, 0),
	}
	writer.inodeCount = 1
	writer.inodeMap["."] = writer.rootInode
	writer.inodeMap[""] = writer.rootInode

	for _, opt := range opts {
		if err := opt(writer); err != nil {
			return nil, err
		}
	}

	return writer, nil
}

func (w *Writer) SetCompression(comp Compression) { w.comp = comp }
func (w *Writer) SetSourceFS(srcFS fs.FS)         { w.srcFS = srcFS }

func (w *Writer) Add(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
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
		srcFS:   w.srcFS,
	}

	if sys := info.Sys(); sys != nil {
		if statT, ok := sys.(interface {
			Uid() uint32
			Gid() uint32
		}); ok {
			inode.uid = statT.Uid()
			inode.gid = statT.Gid()
		}
	}

	switch {
	case info.Mode().IsDir():
		inode.fileType = DirType
		inode.entries = make([]*writerInode, 0)
		inode.nlink = 2
	case info.Mode().IsRegular():
		inode.fileType = FileType
	case info.Mode()&fs.ModeSymlink != 0:
		inode.fileType = SymlinkType
		if inode.srcFS != nil {
			target, err := fs.ReadLink(inode.srcFS, path)
			if err != nil {
				return fmt.Errorf("readlink: %w", err)
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
		inode.fileType = FileType
	}

	w.inodeMap[path] = inode
	parent := w.inodeMap[getParentPath(path)]
	if parent == nil {
		return fmt.Errorf("parent not found: %s", path)
	}
	inode.parent = parent
	parent.entries = append(parent.entries, inode)

	return nil
}

func getParentPath(path string) string {
	if path == "" || path == "." {
		return ""
	}
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

func (w *Writer) Write(p []byte) (n int, err error) {
	if w.wa != nil {
		n, err = w.wa.WriteAt(p, int64(w.offset))
	} else {
		n, err = w.buf.Write(p)
	}
	w.offset += uint64(n)
	return n, err
}

func (w *Writer) write(data []byte) error {
	_, err := w.Write(data)
	return err
}

// Finalize writes the filesystem.
// The approach follows squashfs-tools-ng: inode table streams to output,
// directory table is buffered. For each directory, we write its entries
// BEFORE writing its inode, so we know the directory position.
func (w *Writer) Finalize() error {
	if err := w.buildIDTable(); err != nil {
		return err
	}
	if err := w.writeFileData(); err != nil {
		return err
	}

	// Sort all directory entries for deterministic output
	var sortEntries func(*writerInode)
	sortEntries = func(node *writerInode) {
		if node.fileType == DirType || node.fileType == XDirType {
			sortInodes(node.entries)
			for _, child := range node.entries {
				sortEntries(child)
			}
		}
	}
	sortEntries(w.rootInode)

	// Pre-calculate directory sizes and detect XDirType
	w.calcDirectorySizes(w.rootInode)

	// Create metadata writers:
	// - Inode table streams directly to output (physical positions known immediately)
	// - Directory table is buffered in memory (written after inode table)
	w.inodeTableStart = w.offset
	inodeMeta := newStreamingMetaWriter(w, w.comp)
	dirMeta := newBufferedMetaWriter(w.comp)

	// Serialize all inodes in children-before-parents order.
	// For directories: write directory entries first, then the directory inode.
	if err := w.serializeTree(w.rootInode, inodeMeta, dirMeta); err != nil {
		return err
	}

	// Flush remaining inode data
	if err := inodeMeta.Flush(); err != nil {
		return err
	}

	// Now write the directory table
	w.dirTableStart = w.offset
	if _, err := dirMeta.WriteToOutput(w); err != nil {
		return err
	}

	// Fix up the root inode reference using physical offsets
	rootPhysOffset := inodeMeta.BlockOffset(w.rootInode.inodeBlockIdx)
	w.sb.RootInode = inodeRef((rootPhysOffset << 16) | uint64(w.rootInode.inodeOffset))

	// Write ID table
	if err := w.writeIDTable(); err != nil {
		return err
	}

	w.fragTableStart = 0xFFFFFFFFFFFFFFFF
	w.exportTableStart = 0xFFFFFFFFFFFFFFFF

	w.bytesUsed = w.offset
	w.buildSuperblock()

	// Write superblock at position 0
	sbData := w.sb.Bytes()
	if w.wa != nil {
		if _, err := w.wa.WriteAt(sbData, 0); err != nil {
			return err
		}
	} else {
		data := w.buf.Bytes()
		if len(data) >= SuperblockSize {
			copy(data[0:SuperblockSize], sbData)
		}
		if _, err := w.w.Write(data); err != nil {
			return err
		}
	}

	return nil
}

// calcDirectorySizes recursively calculates directory sizes and detects XDirType.
func (w *Writer) calcDirectorySizes(node *writerInode) {
	if node.fileType != DirType && node.fileType != XDirType {
		return
	}

	// Process children first
	for _, child := range node.entries {
		w.calcDirectorySizes(child)
	}

	// Calculate directory data size
	size := w.calcDirDataSize(node)
	node.size = uint64(size)

	// Check if XDir is needed
	if len(node.entries) > 256 || size > 16384 {
		node.fileType = XDirType
	}
}

// calcDirDataSize calculates directory data size.
// This is a pre-calculation; we don't know exact inode block positions yet,
// so we use a conservative estimate (one header per ~200 entries or inode number break).
func (w *Writer) calcDirDataSize(node *writerInode) int {
	if len(node.entries) == 0 {
		return 12 // Empty directory header
	}

	// Conservative estimate: assume entries might need separate headers
	// due to inode number differences
	size := 0
	currentIdx := 0
	for currentIdx < len(node.entries) {
		baseChild := node.entries[currentIdx]

		endIdx := currentIdx + 1
		for endIdx < len(node.entries) && endIdx-currentIdx < 256 {
			child := node.entries[endIdx]
			diff := int32(child.ino) - int32(baseChild.ino)
			if diff > 32767 || diff < -32768 {
				break
			}
			endIdx++
		}

		// Header
		size += 12
		// Entries
		for i := currentIdx; i < endIdx; i++ {
			size += 8 + len(node.entries[i].name)
		}

		currentIdx = endIdx
	}

	return size
}

// serializeTree writes inodes in children-before-parents order.
// For directories: write directory entries to dirMeta first, then write the inode.
func (w *Writer) serializeTree(node *writerInode, inodeMeta *streamingMetaWriter, dirMeta *bufferedMetaWriter) error {
	// Process children first (post-order traversal)
	if node.fileType == DirType || node.fileType == XDirType {
		for _, child := range node.entries {
			if err := w.serializeTree(child, inodeMeta, dirMeta); err != nil {
				return err
			}
		}
	}

	// For directories: write directory entries BEFORE the inode
	if node.fileType == DirType || node.fileType == XDirType {
		// Get physical position in directory table (compressed block offset + uncompressed offset)
		physOffset, offset := dirMeta.Position()
		node.dirBlockIdx = uint32(physOffset) // Physical byte offset into dir table
		node.dirOffset = offset
		actualSize, err := w.writeDirEntries(node, inodeMeta, dirMeta)
		if err != nil {
			return err
		}
		// Update size to actual bytes written (overrides the pre-calculated estimate)
		node.size = uint64(actualSize)
	}

	// Record inode position, then write the inode
	node.inodeBlockIdx, node.inodeOffset = inodeMeta.Position()
	return w.writeInode(node, inodeMeta)
}

// writeDirEntries writes directory entries to the buffered directory meta.
// Returns the number of bytes written.
func (w *Writer) writeDirEntries(node *writerInode, inodeMeta *streamingMetaWriter, dirMeta *bufferedMetaWriter) (int, error) {
	bytesWritten := 0

	if len(node.entries) == 0 {
		// Empty directory - write a minimal header
		buf := make([]byte, 12)
		binary.LittleEndian.PutUint32(buf[0:], 0)        // count-1 = 0 means 1 entry? Actually 0 entries
		binary.LittleEndian.PutUint32(buf[4:], 0)        // startBlock (unused for empty)
		binary.LittleEndian.PutUint32(buf[8:], node.ino) // inodeNum
		n, _ := dirMeta.Write(buf)
		bytesWritten += n
		return bytesWritten, nil
	}

	currentIdx := 0
	for currentIdx < len(node.entries) {
		// Find entries that share the same inode block and have compatible inode numbers
		baseChild := node.entries[currentIdx]
		baseBlockIdx := baseChild.inodeBlockIdx

		endIdx := currentIdx + 1
		for endIdx < len(node.entries) && endIdx-currentIdx < 256 {
			child := node.entries[endIdx]
			// Must be in the same inode block
			if child.inodeBlockIdx != baseBlockIdx {
				break
			}
			// Inode number difference must fit in int16
			diff := int32(child.ino) - int32(baseChild.ino)
			if diff > 32767 || diff < -32768 {
				break
			}
			endIdx++
		}

		// Get physical block offset for this group's inodes
		startBlock := inodeMeta.BlockOffset(baseBlockIdx)

		// Write header
		headerBuf := make([]byte, 12)
		binary.LittleEndian.PutUint32(headerBuf[0:], uint32(endIdx-currentIdx-1))
		binary.LittleEndian.PutUint32(headerBuf[4:], uint32(startBlock))
		binary.LittleEndian.PutUint32(headerBuf[8:], baseChild.ino)
		n, _ := dirMeta.Write(headerBuf)
		bytesWritten += n

		// Write entries
		for i := currentIdx; i < endIdx; i++ {
			child := node.entries[i]
			entryBuf := &bytes.Buffer{}
			_ = binary.Write(entryBuf, binary.LittleEndian, child.inodeOffset)
			_ = binary.Write(entryBuf, binary.LittleEndian, int16(int32(child.ino)-int32(baseChild.ino)))
			_ = binary.Write(entryBuf, binary.LittleEndian, child.fileType)
			_ = binary.Write(entryBuf, binary.LittleEndian, uint16(len(child.name)-1))
			_, _ = entryBuf.Write([]byte(child.name))
			n, _ := dirMeta.Write(entryBuf.Bytes())
			bytesWritten += n
		}

		currentIdx = endIdx
	}

	return bytesWritten, nil
}

// writeInode writes a single inode to the streaming inode meta.
func (w *Writer) writeInode(node *writerInode, inodeMeta *streamingMetaWriter) error {
	buf := &bytes.Buffer{}
	order := binary.LittleEndian

	// Common header (bytes.Buffer never fails, explicitly ignore errors)
	_ = binary.Write(buf, order, node.fileType)
	_ = binary.Write(buf, order, uint16(node.mode&0777))
	_ = binary.Write(buf, order, uint16(w.idTable[node.uid]))
	_ = binary.Write(buf, order, uint16(w.idTable[node.gid]))
	_ = binary.Write(buf, order, int32(node.modTime))
	_ = binary.Write(buf, order, node.ino)

	switch node.fileType {
	case DirType:
		// For directories, we know the dir position because we wrote entries first
		_ = binary.Write(buf, order, node.dirBlockIdx) // Will be converted to physical later
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint16(node.size))
		_ = binary.Write(buf, order, node.dirOffset)
		parentIno := uint32(1)
		if node.parent != nil {
			parentIno = node.parent.ino
		}
		_ = binary.Write(buf, order, parentIno)

	case XDirType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint32(node.size))
		_ = binary.Write(buf, order, node.dirBlockIdx) // Will be converted to physical later
		parentIno := uint32(1)
		if node.parent != nil {
			parentIno = node.parent.ino
		}
		_ = binary.Write(buf, order, parentIno)
		_ = binary.Write(buf, order, uint16(0))          // index count
		_ = binary.Write(buf, order, node.dirOffset)     // offset
		_ = binary.Write(buf, order, uint32(0xFFFFFFFF)) // xattr index

	case FileType:
		_ = binary.Write(buf, order, uint32(node.startBlock))
		_ = binary.Write(buf, order, uint32(0xFFFFFFFF)) // fragment block
		_ = binary.Write(buf, order, uint32(0))          // fragment offset
		_ = binary.Write(buf, order, uint32(node.size))
		for _, blockSize := range node.dataBlocks {
			_ = binary.Write(buf, order, blockSize)
		}

	case SymlinkType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint32(len(node.symTarget)))
		_, _ = buf.Write([]byte(node.symTarget))

	case CharDevType, BlockDevType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint32(0))

	case FifoType, SocketType:
		_ = binary.Write(buf, order, node.nlink)
	}

	_, err := inodeMeta.Write(buf.Bytes())
	return err
}

// Helper functions

func (w *Writer) buildIDTable() error {
	seen := make(map[uint32]bool)
	w.idList = make([]uint32, 0)
	for _, inode := range w.inodeMap {
		if !seen[inode.uid] {
			seen[inode.uid] = true
			w.idList = append(w.idList, inode.uid)
		}
		if !seen[inode.gid] {
			seen[inode.gid] = true
			w.idList = append(w.idList, inode.gid)
		}
	}
	sort.Slice(w.idList, func(i, j int) bool { return w.idList[i] < w.idList[j] })
	for i, id := range w.idList {
		w.idTable[id] = uint32(i)
	}
	return nil
}

func (w *Writer) writeIDTable() error {
	idData := make([]byte, len(w.idList)*4)
	for i, id := range w.idList {
		binary.LittleEndian.PutUint32(idData[i*4:], id)
	}

	// Write ID data as metadata block
	blockStart := w.offset
	header := make([]byte, 2)
	binary.LittleEndian.PutUint16(header, uint16(len(idData))|0x8000) // Uncompressed
	if err := w.write(header); err != nil {
		return err
	}
	if err := w.write(idData); err != nil {
		return err
	}

	// Write pointer to ID block
	w.idTableStart = w.offset
	pointer := make([]byte, 8)
	binary.LittleEndian.PutUint64(pointer, blockStart)
	return w.write(pointer)
}

func (w *Writer) writeFileData() error {
	var paths []string
	for p := range w.inodeMap {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		inode := w.inodeMap[p]
		if inode.fileType != FileType || inode.size == 0 || inode.srcFS == nil {
			continue
		}

		data, err := fs.ReadFile(inode.srcFS, inode.path)
		if err != nil {
			return err
		}

		inode.startBlock = w.offset
		blockSize := int(w.blockSize)
		inode.dataBlocks = make([]uint32, 0)

		for offset := 0; offset < len(data); offset += blockSize {
			end := offset + blockSize
			if end > len(data) {
				end = len(data)
			}
			block := data[offset:end]

			compressed, _ := w.comp.compress(block)
			if compressed != nil && len(compressed) < len(block) {
				if err := w.write(compressed); err != nil {
					return err
				}
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(compressed)))
			} else {
				if err := w.write(block); err != nil {
					return err
				}
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(block))|0x01000000)
			}
		}
	}
	return nil
}

func sortInodes(inodes []*writerInode) {
	sort.Slice(inodes, func(i, j int) bool {
		return inodes[i].name < inodes[j].name
	})
}

func (w *Writer) buildSuperblock() {
	blockLog := uint16(0)
	for i := uint16(0); i < 32; i++ {
		if (1 << i) == w.blockSize {
			blockLog = i
			break
		}
	}
	w.sb.Magic = 0x73717368
	w.sb.InodeCnt = w.inodeCount
	w.sb.ModTime = w.modTime
	w.sb.BlockSize = w.blockSize
	w.sb.FragCount = 0
	w.sb.Comp = w.comp
	w.sb.BlockLog = blockLog
	w.sb.Flags = w.flags
	w.sb.IdCount = uint16(len(w.idList))
	w.sb.VMajor = 4
	w.sb.VMinor = 0
	w.sb.BytesUsed = w.bytesUsed
	w.sb.IdTableStart = w.idTableStart
	w.sb.XattrIdTableStart = 0xFFFFFFFFFFFFFFFF
	w.sb.InodeTableStart = w.inodeTableStart
	w.sb.DirTableStart = w.dirTableStart
	w.sb.FragTableStart = w.fragTableStart
	w.sb.ExportTableStart = w.exportTableStart
	w.sb.order = binary.LittleEndian
}
