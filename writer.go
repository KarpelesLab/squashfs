package squashfs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"path"
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

	// Fragment accumulation
	fragEntries   []fragEntry
	fragBuf       []byte
	fragBufInodes []*writerInode

	// Hard link detection (for AddFS dedup)
	hardLinkMap map[devIno]*writerInode

	// Table positions (filled during Finalize)
	idTableStart     uint64
	inodeTableStart  uint64
	dirTableStart    uint64
	fragTableStart   uint64
	xattrTableStart  uint64
	exportTableStart uint64
	bytesUsed        uint64

	sb Superblock
}

// fragEntry describes a completed fragment block on disk.
type fragEntry struct {
	start uint64 // byte offset where the fragment block was written
	size  uint32 // compressed size, bit 24 set if uncompressed
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
	rdev      uint32
	fileData  []byte

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
	fragBlock  uint32 // fragment table index (0xFFFFFFFF = no fragment)
	fragOfft   uint32 // offset within decompressed fragment block

	// Deduplication
	cloneOf *writerInode // if set, shares file data with this inode

	// Extended attributes
	xattrs   map[string][]byte
	xattrIdx uint32 // index into xattr ID table (0xFFFFFFFF = none)
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
		w:           w,
		blockSize:   131072, // 128KB default
		comp:        GZip,
		modTime:     int32(time.Now().Unix()),
		idTable:     make(map[uint32]uint32),
		inodeMap:    make(map[string]*writerInode),
		hardLinkMap: make(map[devIno]*writerInode),
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
		ino:       1,
		mode:      fs.ModeDir | 0755,
		modTime:   time.Now().Unix(),
		nlink:     2,
		fileType:  DirType,
		entries:   make([]*writerInode, 0),
		fragBlock: 0xFFFFFFFF,
		xattrIdx:  0xFFFFFFFF,
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

// AddFS walks the given filesystem and adds all entries to the writer.
func (w *Writer) AddFS(srcFS fs.FS) error {
	return fs.WalkDir(srcFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || p == "" {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		w.inodeCount++
		inode := &writerInode{
			path:      p,
			name:      info.Name(),
			ino:       w.inodeCount,
			mode:      info.Mode(),
			size:      uint64(info.Size()),
			modTime:   info.ModTime().Unix(),
			nlink:     1,
			srcFS:     srcFS,
			fragBlock: 0xFFFFFFFF,
			xattrIdx:  0xFFFFFFFF,
		}

		if sys := info.Sys(); sys != nil {
			if statT, ok := sys.(interface {
				Uid() uint32
				Gid() uint32
			}); ok {
				inode.uid = statT.Uid()
				inode.gid = statT.Gid()
			}
			if statT, ok := sys.(interface {
				Rdev() uint32
			}); ok {
				inode.rdev = statT.Rdev()
			}
		}

		// Hard link detection for regular files
		if info.Mode().IsRegular() {
			if di, ok := getDevIno(info.Sys()); ok {
				if existing, found := w.hardLinkMap[di]; found {
					inode.cloneOf = existing
					inode.nlink = existing.nlink + 1
					existing.nlink = inode.nlink
				} else {
					w.hardLinkMap[di] = inode
				}
			}
		}

		// Read xattrs from source filesystem
		if xattrs := readXattrs(srcFS, p); len(xattrs) > 0 {
			inode.xattrs = xattrs
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
			target, err := fs.ReadLink(srcFS, p)
			if err != nil {
				return fmt.Errorf("readlink: %w", err)
			}
			inode.symTarget = target
			inode.size = uint64(len(target))
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

		w.inodeMap[p] = inode
		parent := w.inodeMap[getParentPath(p)]
		if parent == nil {
			return fmt.Errorf("parent not found: %s", p)
		}
		inode.parent = parent
		parent.entries = append(parent.entries, inode)

		return nil
	})
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

// addInode adds a pre-built inode to the writer's tree, creating intermediate
// directories as needed.
func (w *Writer) addInode(p string, inode *writerInode) error {
	if p == "" || p == "." {
		return fmt.Errorf("cannot add root inode")
	}
	if _, exists := w.inodeMap[p]; exists {
		return fmt.Errorf("path already exists: %s", p)
	}

	// Ensure parent directories exist
	parentPath := getParentPath(p)
	if parentPath != "" && parentPath != "." {
		if _, exists := w.inodeMap[parentPath]; !exists {
			// Auto-create intermediate directory
			if err := w.AddDirectory(parentPath, 0755); err != nil {
				return err
			}
		}
	}

	parent := w.inodeMap[parentPath]
	if parent == nil {
		return fmt.Errorf("parent not found: %s", parentPath)
	}

	w.inodeCount++
	inode.ino = w.inodeCount
	inode.path = p
	inode.name = path.Base(p)
	inode.parent = parent
	inode.fragBlock = 0xFFFFFFFF
	inode.xattrIdx = 0xFFFFFFFF
	if inode.modTime == 0 {
		inode.modTime = time.Now().Unix()
	}

	w.inodeMap[p] = inode
	parent.entries = append(parent.entries, inode)
	return nil
}

// AddFile adds a regular file with the given content and permissions.
// The perm may include fs.ModeSetuid, fs.ModeSetgid, and fs.ModeSticky.
func (w *Writer) AddFile(p string, data []byte, perm fs.FileMode) error {
	inode := &writerInode{
		mode:     perm & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky),
		size:     uint64(len(data)),
		fileType: FileType,
		nlink:    1,
		fileData: data,
	}
	return w.addInode(p, inode)
}

// AddDirectory adds an empty directory with the given permissions.
func (w *Writer) AddDirectory(p string, perm fs.FileMode) error {
	inode := &writerInode{
		mode:     fs.ModeDir | perm&fs.ModePerm,
		fileType: DirType,
		nlink:    2,
		entries:  make([]*writerInode, 0),
	}
	return w.addInode(p, inode)
}

// AddSymlink adds a symbolic link pointing to target.
func (w *Writer) AddSymlink(p, target string) error {
	inode := &writerInode{
		mode:      fs.ModeSymlink | 0777,
		size:      uint64(len(target)),
		fileType:  SymlinkType,
		nlink:     1,
		symTarget: target,
	}
	return w.addInode(p, inode)
}

// AddDevice adds a block or character device node. The mode must include
// fs.ModeDevice (block) or fs.ModeCharDevice (character) to select the type.
func (w *Writer) AddDevice(p string, mode fs.FileMode, rdev uint32) error {
	var ft Type
	switch {
	case mode&fs.ModeCharDevice != 0:
		ft = CharDevType
	case mode&fs.ModeDevice != 0:
		ft = BlockDevType
	default:
		return fmt.Errorf("mode must include ModeDevice or ModeCharDevice")
	}
	inode := &writerInode{
		mode:     mode,
		fileType: ft,
		nlink:    1,
		rdev:     rdev,
	}
	return w.addInode(p, inode)
}

// AddFifo adds a named pipe (FIFO) with the given permissions.
func (w *Writer) AddFifo(p string, perm fs.FileMode) error {
	inode := &writerInode{
		mode:     fs.ModeNamedPipe | perm&fs.ModePerm,
		fileType: FifoType,
		nlink:    1,
	}
	return w.addInode(p, inode)
}

// AddSocket adds a Unix domain socket with the given permissions.
func (w *Writer) AddSocket(p string, perm fs.FileMode) error {
	inode := &writerInode{
		mode:     fs.ModeSocket | perm&fs.ModePerm,
		fileType: SocketType,
		nlink:    1,
	}
	return w.addInode(p, inode)
}

// SetOwner sets the uid and gid on an existing inode.
func (w *Writer) SetOwner(p string, uid, gid uint32) error {
	inode := w.inodeMap[p]
	if inode == nil {
		return fmt.Errorf("path not found: %s", p)
	}
	inode.uid = uid
	inode.gid = gid
	return nil
}

// SetModTime sets the modification time on an existing inode.
func (w *Writer) SetModTime(p string, t time.Time) error {
	inode := w.inodeMap[p]
	if inode == nil {
		return fmt.Errorf("path not found: %s", p)
	}
	inode.modTime = t.Unix()
	return nil
}

// CloneInode creates a new entry at newPath that references the same underlying
// inode as existingPath. This works for any inode type — files share data blocks,
// directories share directory entries, symlinks share targets, etc.
// The caller is responsible for not creating cycles (e.g. cloning a directory into itself).
func (w *Writer) CloneInode(newPath, existingPath string) error {
	existing := w.inodeMap[existingPath]
	if existing == nil {
		return fmt.Errorf("path not found: %s", existingPath)
	}

	inode := &writerInode{
		mode:      existing.mode,
		size:      existing.size,
		fileType:  existing.fileType,
		symTarget: existing.symTarget,
		rdev:      existing.rdev,
		nlink:     1,
		cloneOf:   existing,
	}
	if existing.fileType.IsDir() {
		inode.entries = existing.entries
	}
	if err := w.addInode(newPath, inode); err != nil {
		return err
	}

	existing.nlink++
	inode.nlink = existing.nlink
	return nil
}

// SetXattr sets an extended attribute on an existing inode.
func (w *Writer) SetXattr(p, name string, value []byte) error {
	inode := w.inodeMap[p]
	if inode == nil {
		return fmt.Errorf("path not found: %s", p)
	}
	if inode.xattrs == nil {
		inode.xattrs = make(map[string][]byte)
	}
	inode.xattrs[name] = value
	return nil
}

// promoteExtendedTypes upgrades inodes to extended types when needed.
func (w *Writer) promoteExtendedTypes() {
	for _, inode := range w.inodeMap {
		needsExtended := len(inode.xattrs) > 0 || inode.xattrIdx != 0xFFFFFFFF
		needsExtended = needsExtended || (inode.fileType == FileType && inode.nlink > 1)

		if !needsExtended {
			continue
		}

		switch inode.fileType {
		case FileType:
			inode.fileType = XFileType
		case SymlinkType:
			inode.fileType = XSymlinkType
		case BlockDevType:
			inode.fileType = XBlockDevType
		case CharDevType:
			inode.fileType = XCharDevType
		case FifoType:
			inode.fileType = XFifoType
		case SocketType:
			inode.fileType = XSocketType
			// DirType → XDirType is handled by calcDirectorySizes
		}
	}
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

	// Assign xattr indices (needed before type promotion and inode serialization)
	w.assignXattrIndices()

	// Promote inodes to extended types where needed (xattrs, nlink>1)
	w.promoteExtendedTypes()

	// Sort all directory entries for deterministic output
	var sortEntries func(*writerInode)
	sortEntries = func(node *writerInode) {
		if node.fileType.Basic() == DirType {
			sortInodes(node.entries)
			for _, child := range node.entries {
				sortEntries(child)
			}
		}
	}
	sortEntries(w.rootInode)

	// Pre-calculate directory sizes and detect XDirType
	w.calcDirectorySizes(w.rootInode)

	// Write tables in squashfs order: inode → directory → fragment → ID
	w.inodeTableStart = w.offset
	inodeMeta := newStreamingMetaWriter(w, w.comp)
	dirMeta := newBufferedMetaWriter(w.comp)

	if err := w.serializeTree(w.rootInode, inodeMeta, dirMeta); err != nil {
		return err
	}
	if err := inodeMeta.Flush(); err != nil {
		return err
	}

	w.dirTableStart = w.offset
	if _, err := dirMeta.WriteToOutput(w); err != nil {
		return err
	}

	rootPhysOffset := inodeMeta.BlockOffset(w.rootInode.inodeBlockIdx)
	w.sb.RootInode = inodeRef((rootPhysOffset << 16) | uint64(w.rootInode.inodeOffset))

	if err := w.writeFragmentTable(); err != nil {
		return err
	}

	w.exportTableStart = 0xFFFFFFFFFFFFFFFF

	if err := w.writeIDTable(); err != nil {
		return err
	}

	if err := w.writeXattrTable(); err != nil {
		return err
	}

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
	if !node.fileType.IsDir() {
		return
	}

	// Process children first
	for _, child := range node.entries {
		w.calcDirectorySizes(child)
	}

	// Calculate directory data size (stored as actual + 3 per squashfs convention)
	size := w.calcDirDataSize(node) + 3
	node.size = uint64(size)

	// Check if XDir is needed (compare against stored size which includes +3)
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
	if node.fileType.IsDir() {
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
		// The squashfs spec stores directory size as actual_data + 3
		// (the reader stops when exactly 3 bytes remain).
		node.size = uint64(actualSize) + 3
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
		// Empty directory - no data to write.
		// Size will be set to 3 (the squashfs convention) by the caller.
		return 0, nil
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
			_ = binary.Write(entryBuf, binary.LittleEndian, child.fileType.Basic())
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
	_ = binary.Write(buf, order, uint16(modeToUnix(node.mode)&0xFFF))
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
		_ = binary.Write(buf, order, uint16(0))      // index count
		_ = binary.Write(buf, order, node.dirOffset) // offset
		_ = binary.Write(buf, order, node.xattrIdx)

	case FileType:
		_ = binary.Write(buf, order, uint32(node.startBlock))
		_ = binary.Write(buf, order, node.fragBlock)
		_ = binary.Write(buf, order, node.fragOfft)
		_ = binary.Write(buf, order, uint32(node.size))
		for _, blockSize := range node.dataBlocks {
			_ = binary.Write(buf, order, blockSize)
		}

	case XFileType:
		_ = binary.Write(buf, order, node.startBlock) // uint64
		_ = binary.Write(buf, order, node.size)       // uint64
		_ = binary.Write(buf, order, uint64(0))       // sparse
		_ = binary.Write(buf, order, node.nlink)      // uint32
		_ = binary.Write(buf, order, node.fragBlock)  // uint32
		_ = binary.Write(buf, order, node.fragOfft)   // uint32
		_ = binary.Write(buf, order, node.xattrIdx)   // uint32
		for _, blockSize := range node.dataBlocks {
			_ = binary.Write(buf, order, blockSize)
		}

	case SymlinkType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint32(len(node.symTarget)))
		_, _ = buf.Write([]byte(node.symTarget))

	case XSymlinkType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, uint32(len(node.symTarget)))
		_, _ = buf.Write([]byte(node.symTarget))
		_ = binary.Write(buf, order, node.xattrIdx)

	case CharDevType, BlockDevType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, node.rdev)

	case XCharDevType, XBlockDevType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, node.rdev)
		_ = binary.Write(buf, order, node.xattrIdx)

	case FifoType, SocketType:
		_ = binary.Write(buf, order, node.nlink)

	case XFifoType, XSocketType:
		_ = binary.Write(buf, order, node.nlink)
		_ = binary.Write(buf, order, node.xattrIdx)
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

	blockSize := int(w.blockSize)

	for _, p := range paths {
		inode := w.inodeMap[p]
		if inode.fileType != FileType || inode.size == 0 || inode.cloneOf != nil {
			continue
		}

		var data []byte
		if inode.fileData != nil {
			data = inode.fileData
		} else if inode.srcFS != nil {
			var err error
			data, err = fs.ReadFile(inode.srcFS, inode.path)
			if err != nil {
				return err
			}
		} else {
			continue
		}

		inode.startBlock = w.offset
		inode.dataBlocks = make([]uint32, 0)

		fullBlocks := len(data) / blockSize
		tailSize := len(data) % blockSize

		// Write full blocks
		for i := 0; i < fullBlocks; i++ {
			block := data[i*blockSize : (i+1)*blockSize]
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

		// Handle tail as fragment
		if tailSize > 0 {
			tail := data[fullBlocks*blockSize:]

			// Flush if adding this tail would exceed blockSize
			if len(w.fragBuf)+len(tail) > blockSize {
				if err := w.flushFragmentBlock(); err != nil {
					return err
				}
			}

			inode.fragOfft = uint32(len(w.fragBuf))
			w.fragBuf = append(w.fragBuf, tail...)
			w.fragBufInodes = append(w.fragBufInodes, inode)
		}
	}

	// Flush remaining fragment data
	if len(w.fragBuf) > 0 {
		if err := w.flushFragmentBlock(); err != nil {
			return err
		}
	}

	// Copy data references for cloned (hard-linked) inodes
	for _, p := range paths {
		inode := w.inodeMap[p]
		if inode.cloneOf != nil {
			inode.startBlock = inode.cloneOf.startBlock
			inode.dataBlocks = inode.cloneOf.dataBlocks
			inode.fragBlock = inode.cloneOf.fragBlock
			inode.fragOfft = inode.cloneOf.fragOfft
		}
	}

	return nil
}

// flushFragmentBlock compresses and writes the accumulated fragment buffer.
func (w *Writer) flushFragmentBlock() error {
	if len(w.fragBuf) == 0 {
		return nil
	}

	fragIdx := uint32(len(w.fragEntries))
	start := w.offset

	compressed, _ := w.comp.compress(w.fragBuf)
	var size uint32
	if compressed != nil && len(compressed) < len(w.fragBuf) {
		if err := w.write(compressed); err != nil {
			return err
		}
		size = uint32(len(compressed))
	} else {
		if err := w.write(w.fragBuf); err != nil {
			return err
		}
		size = uint32(len(w.fragBuf)) | 0x01000000
	}

	w.fragEntries = append(w.fragEntries, fragEntry{start: start, size: size})

	// Assign fragment index to all pending inodes
	for _, inode := range w.fragBufInodes {
		inode.fragBlock = fragIdx
	}

	w.fragBuf = w.fragBuf[:0]
	w.fragBufInodes = w.fragBufInodes[:0]
	return nil
}

// writeFragmentTable writes the fragment table and its indirect pointer table.
func (w *Writer) writeFragmentTable() error {
	if len(w.fragEntries) == 0 {
		w.fragTableStart = 0xFFFFFFFFFFFFFFFF
		return nil
	}

	// Serialize fragment entries (16 bytes each) into metadata blocks
	entryData := make([]byte, len(w.fragEntries)*16)
	for i, e := range w.fragEntries {
		binary.LittleEndian.PutUint64(entryData[i*16:], e.start)
		binary.LittleEndian.PutUint32(entryData[i*16+8:], e.size)
		binary.LittleEndian.PutUint32(entryData[i*16+12:], 0) // unused
	}

	// Write as compressed metadata blocks (8KB each), track block offsets
	var blockOffsets []uint64
	for pos := 0; pos < len(entryData); pos += metaBlockSize {
		end := pos + metaBlockSize
		if end > len(entryData) {
			end = len(entryData)
		}
		block := entryData[pos:end]

		blockOffsets = append(blockOffsets, w.offset)
		compressed, _ := w.comp.compress(block)
		header := make([]byte, 2)
		if compressed != nil && len(compressed) < len(block) {
			binary.LittleEndian.PutUint16(header, uint16(len(compressed)))
			if err := w.write(header); err != nil {
				return err
			}
			if err := w.write(compressed); err != nil {
				return err
			}
		} else {
			binary.LittleEndian.PutUint16(header, uint16(len(block))|0x8000)
			if err := w.write(header); err != nil {
				return err
			}
			if err := w.write(block); err != nil {
				return err
			}
		}
	}

	// Write indirect pointer table
	w.fragTableStart = w.offset
	for _, off := range blockOffsets {
		pointer := make([]byte, 8)
		binary.LittleEndian.PutUint64(pointer, off)
		if err := w.write(pointer); err != nil {
			return err
		}
	}

	return nil
}

// xattrSet holds serialized xattr data for one inode.
type xattrSet struct {
	inode   *writerInode
	kvData  []byte
	kvCount uint32
	kvSize  uint32
}

// assignXattrIndices pre-assigns xattr indices to inodes with xattrs.
// Must be called before promoteExtendedTypes and inode serialization.
func (w *Writer) assignXattrIndices() {
	idx := uint32(0)
	for _, inode := range w.inodeMap {
		if len(inode.xattrs) > 0 {
			inode.xattrIdx = idx
			idx++
		}
	}
}

// collectXattrSets builds the serialized xattr data for all inodes.
func (w *Writer) collectXattrSets() []xattrSet {
	var sets []xattrSet
	for _, inode := range w.inodeMap {
		if len(inode.xattrs) == 0 {
			continue
		}

		var kvBuf bytes.Buffer
		count := uint32(0)
		var keys []string
		for k := range inode.xattrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			val := inode.xattrs[key]
			typ, name, err := xattrParseKey(key)
			if err != nil {
				continue
			}
			_ = binary.Write(&kvBuf, binary.LittleEndian, typ)
			_ = binary.Write(&kvBuf, binary.LittleEndian, uint16(len(name)))
			kvBuf.Write([]byte(name))
			_ = binary.Write(&kvBuf, binary.LittleEndian, uint32(len(val)))
			kvBuf.Write(val)
			count++
		}

		if count > 0 {
			sets = append(sets, xattrSet{
				inode:   inode,
				kvData:  kvBuf.Bytes(),
				kvCount: count,
				kvSize:  uint32(kvBuf.Len()),
			})
		}
	}
	return sets
}

// writeXattrTable serializes extended attributes into the squashfs xattr table format.
func (w *Writer) writeXattrTable() error {
	sets := w.collectXattrSets()

	if len(sets) == 0 {
		w.xattrTableStart = 0xFFFFFFFFFFFFFFFF
		return nil
	}

	// Write k-v data as compressed metadata blocks
	kvMeta := newBufferedMetaWriter(w.comp)
	type idEntry struct {
		ref   uint64
		count uint32
		size  uint32
	}
	var idEntries []idEntry

	for i := range sets {
		physOffset, offset := kvMeta.Position()
		ref := (physOffset << 16) | uint64(offset)

		kvMeta.Write(sets[i].kvData)

		idEntries = append(idEntries, idEntry{
			ref:   ref,
			count: sets[i].kvCount,
			size:  sets[i].kvSize,
		})
	}

	// Write k-v metadata blocks to output
	kvStart := w.offset
	if _, err := kvMeta.WriteToOutput(w); err != nil {
		return err
	}

	// Write ID entries as compressed metadata blocks
	idData := make([]byte, len(idEntries)*16)
	for i, e := range idEntries {
		binary.LittleEndian.PutUint64(idData[i*16:], e.ref)
		binary.LittleEndian.PutUint32(idData[i*16+8:], e.count)
		binary.LittleEndian.PutUint32(idData[i*16+12:], e.size)
	}

	var idBlockOffsets []uint64
	for pos := 0; pos < len(idData); pos += metaBlockSize {
		end := pos + metaBlockSize
		if end > len(idData) {
			end = len(idData)
		}
		block := idData[pos:end]

		idBlockOffsets = append(idBlockOffsets, w.offset)
		compressed, _ := w.comp.compress(block)
		header := make([]byte, 2)
		if compressed != nil && len(compressed) < len(block) {
			binary.LittleEndian.PutUint16(header, uint16(len(compressed)))
			if err := w.write(header); err != nil {
				return err
			}
			if err := w.write(compressed); err != nil {
				return err
			}
		} else {
			binary.LittleEndian.PutUint16(header, uint16(len(block))|0x8000)
			if err := w.write(header); err != nil {
				return err
			}
			if err := w.write(block); err != nil {
				return err
			}
		}
	}

	// Write xattr table header
	w.xattrTableStart = w.offset
	// u64 kv_start
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, kvStart)
	if err := w.write(buf); err != nil {
		return err
	}
	// u32 count
	buf = make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(idEntries)))
	if err := w.write(buf); err != nil {
		return err
	}
	// u32 unused
	buf = make([]byte, 4)
	if err := w.write(buf); err != nil {
		return err
	}
	// u64[] id block locations
	for _, off := range idBlockOffsets {
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, off)
		if err := w.write(buf); err != nil {
			return err
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
	w.sb.FragCount = uint32(len(w.fragEntries))
	w.sb.Comp = w.comp
	w.sb.BlockLog = blockLog
	w.sb.Flags = w.flags
	w.sb.IdCount = uint16(len(w.idList))
	w.sb.VMajor = 4
	w.sb.VMinor = 0
	w.sb.BytesUsed = w.bytesUsed
	w.sb.IdTableStart = w.idTableStart
	w.sb.XattrIdTableStart = w.xattrTableStart
	w.sb.InodeTableStart = w.inodeTableStart
	w.sb.DirTableStart = w.dirTableStart
	w.sb.FragTableStart = w.fragTableStart
	w.sb.ExportTableStart = w.exportTableStart
	w.sb.order = binary.LittleEndian
}
