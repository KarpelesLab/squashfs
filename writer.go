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

const (
	maxMetadataBlockSize = 8192
	metadataHeaderSize   = 2
	// For uncompressed blocks, the on-disk size is DataSize + HeaderSize
	// Full blocks are 8192 + 2 = 8194 bytes
	fullBlockPhysicalSize = maxMetadataBlockSize + metadataHeaderSize
)

// Writer creates SquashFS filesystem images.
// This implementation uses uncompressed metadata to resolve circular dependencies
// between Inodes and Directory Tables deterministically.
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

	// Calculated positions (Uncompressed offsets)
	inodeUncompOffset uint32
	dirUncompOffset   uint32
	
	// Directory Index (calculated during layout)
	dirIndex []DirIndexEntry

		// File data info
		dataBlocks []uint32
		startBlock uint64
		
		// Temporary Physical Fields (used during writing)
		inodeRef       inodeRef
		dirBlockStart  uint32
		dirBlockOffset uint16
	}
	
	// WriterOption configures a Writer
	type WriterOption func(*Writer) error
	
	func WithBlockSize(size uint32) WriterOption {	return func(w *Writer) error {
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
	if err != nil { return err }
	if path == "." || path == "" { return nil }

	info, err := d.Info()
	if err != nil { return err }

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
			if err != nil { return fmt.Errorf("readlink: %w", err) }
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
	if parent == nil { return fmt.Errorf("parent not found: %s", path) }
	inode.parent = parent
	parent.entries = append(parent.entries, inode)

	return nil
}

func getParentPath(path string) string {
	if path == "" || path == "." { return "" }
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 { return "." }
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
func (w *Writer) Finalize() error {
	if err := w.buildIDTable(); err != nil { return err }
	if err := w.writeFileData(); err != nil { return err } // Write file content

	// Layout Pass: Calculate uncompressed sizes and offsets
	var dirUncompOffset uint32
	var inodeUncompOffset uint32
	
	// Helper to traverse and calculate
	var calcLayout func(*writerInode) error
	calcLayout = func(node *writerInode) error {
		// Process children first
		if node.fileType == DirType || node.fileType == XDirType {
			sortInodes(node.entries)
			for _, child := range node.entries {
				if err := calcLayout(child); err != nil { return err }
			}
			
			// Calculate Directory Data Size
			dirSize, indexSize, err := w.calcDirSize(node, dirUncompOffset)
			if err != nil { return err }
			
			node.dirUncompOffset = dirUncompOffset
			node.size = uint64(dirSize)
			
			// Check for XDir
			if len(node.entries) > 256 || dirSize > 16384 {
				node.fileType = XDirType
				if indexSize > 0 {
					_, _, err := w.calcDirSize(node, dirUncompOffset) // Refresh
					if err != nil { return err }
				}
			}
			
			dirUncompOffset += uint32(dirSize)
		}
		
		return nil
	}
	
	var calcInodeLayout func(*writerInode) error
	calcInodeLayout = func(node *writerInode) error {
		if node.fileType == DirType || node.fileType == XDirType {
			for _, child := range node.entries {
				if err := calcInodeLayout(child); err != nil { return err }
			}
		}
		
		size, err := w.serializedInodeSize(node)
		if err != nil { return err }
		
		node.inodeUncompOffset = inodeUncompOffset
		inodeUncompOffset += uint32(size)
		return nil
	}

	// Iterate until layout stabilizes
	for i := 0; i < 10; i++ {
		// Reset offsets
		dirUncompOffset = 0
		inodeUncompOffset = 0
		changed := false
		
		// Pass 1: Directories
		err := calcLayout(w.rootInode)
		if err != nil { return err }
		
		// Pass 2: Inodes
		prevInodeTableSize := inodeUncompOffset
		
		err = calcInodeLayout(w.rootInode)
		if err != nil { return err }
		
		if inodeUncompOffset != prevInodeTableSize {
			changed = true
		}
		
		if !changed && i > 0 {
			break
		}
	}
	
	// Phase 3: Write Tables (Uncompressed)
	// We can now write streams using the calculated offsets to generate references.
	
	// Write Inode Table
	w.inodeTableStart = w.offset
	inodeBuf := &bytes.Buffer{}
	if err := w.writeInodeTable(w.rootInode, inodeBuf); err != nil { return err }
	if err := w.writeMetadataStream(inodeBuf.Bytes()); err != nil { return err }
	
	// Write Directory Table
	w.dirTableStart = w.offset
	dirBuf := &bytes.Buffer{}
	if err := w.writeDirTable(w.rootInode, dirBuf); err != nil { return err }
	if err := w.writeMetadataStream(dirBuf.Bytes()); err != nil { return err }
	
	// Finish
	if err := w.writeIDTable(); err != nil { return err }
	
	w.fragTableStart = 0xFFFFFFFFFFFFFFFF
	w.exportTableStart = 0xFFFFFFFFFFFFFFFF
	
	// Calculate Root Inode Ref
	// Physical Block = inodeUncompOffset / 8192 * 8194
	// Offset = inodeUncompOffset % 8192
	// But `inodeUncompOffset` is relative to start of table.
	// We need `PhysicalOffset` relative to start of table.
	// Map logic:
	blockIdx := w.rootInode.inodeUncompOffset / maxMetadataBlockSize
	blockOff := w.rootInode.inodeUncompOffset % maxMetadataBlockSize
	physBlockOff := blockIdx * fullBlockPhysicalSize // This is the OFFSET of the block start
	// Inode Ref = (BlockStartOffset << 16) | InnerOffset
	w.sb.RootInode = inodeRef((uint64(physBlockOff) << 16) | uint64(blockOff))
	
	w.bytesUsed = w.offset
	w.buildSuperblock()
	
	// Final commit
	sbData := w.sb.Bytes()
	if w.wa != nil {
		w.wa.WriteAt(sbData, 0)
	} else {
		// Buffered
		data := w.buf.Bytes()
		if len(data) >= SuperblockSize {
			copy(data[0:SuperblockSize], sbData)
		}
		w.w.Write(data)
	}
	
	return nil
}

// Helpers for Layout/Writing

func (w *Writer) calcDirSize(node *writerInode, startOffset uint32) (int, int, error) {
	// Simulate directory writing
	size := 0
	indexSize := 0
	node.dirIndex = nil
	
	if len(node.entries) == 0 {
		return 12, 0, nil // Header (4+4+4)
	}
	
	currentIndex := 0
	for currentIndex < len(node.entries) {
		// Find run
		baseRef := w.mapUncompToRef(node.entries[currentIndex].inodeUncompOffset)
		baseBlock := baseRef.Index()
		headerBaseIno := node.entries[currentIndex].ino
		
		endIndex := currentIndex
		for endIndex < len(node.entries) {
			ref := w.mapUncompToRef(node.entries[endIndex].inodeUncompOffset)
			if ref.Index() != baseBlock { break }
			if endIndex - currentIndex >= 256 { break }
			diff := int32(node.entries[endIndex].ino) - int32(headerBaseIno)
			if diff > 32767 || diff < -32768 { break }
			endIndex++
		}
		
		// Record Index
		if len(node.entries) > 256 || size > 16384 {
			// This is an XDir.
			// Calculate physical block offset for the *current* directory position
			// currentUncomp = startOffset + size
			physBlock, _ := w.mapUncompToPhysical(startOffset + uint32(size))
			node.dirIndex = append(node.dirIndex, DirIndexEntry{
				Index: uint32(size),
				Start: physBlock,
				Name: node.entries[currentIndex].name,
			})
			indexSize += 12 + len(node.entries[currentIndex].name)
		}
		
		// Header size: 12 bytes
		size += 12
		
		// Entries
		for i := currentIndex; i < endIndex; i++ {
			// Entry size: 8 + nameLen
			size += 8 + len(node.entries[i].name)
		}
		
		currentIndex = endIndex
	}
	
	return size, indexSize, nil
}

func (w *Writer) mapUncompToPhysical(uncomp uint32) (uint32, uint16) {
	blockIdx := uncomp / maxMetadataBlockSize
	offset := uncomp % maxMetadataBlockSize
	phys := blockIdx * fullBlockPhysicalSize
	return phys, uint16(offset)
}

func (w *Writer) mapUncompToRef(uncomp uint32) inodeRef {
	phys, off := w.mapUncompToPhysical(uncomp)
	return inodeRef((uint64(phys) << 16) | uint64(off))
}

func (w *Writer) serializedInodeSize(node *writerInode) (int, error) {
	// Populate temp fields for serialization size check
	// We need these to be valid types, values don't affect size
	node.inodeRef = 0
	physDir, offDir := w.mapUncompToPhysical(node.dirUncompOffset)
	node.dirBlockStart = physDir
	node.dirBlockOffset = offDir
	
	b, err := w.serializeInode(node)
	return len(b), err
}

func (w *Writer) writeMetadataStream(data []byte) error {
	// Chunk and write with uncompressed headers
	for len(data) > 0 {
		n := len(data)
		if n > maxMetadataBlockSize { n = maxMetadataBlockSize }
		chunk := data[:n]
		
		header := make([]byte, 2)
		binary.LittleEndian.PutUint16(header, uint16(n)|0x8000)
		if err := w.write(header); err != nil { return err }
		if err := w.write(chunk); err != nil { return err }
		
		data = data[n:]
	}
	return nil
}

func (w *Writer) writeInodeTable(node *writerInode, buf *bytes.Buffer) error {
	if node.fileType == DirType || node.fileType == XDirType {
		for _, child := range node.entries {
			if err := w.writeInodeTable(child, buf); err != nil { return err }
		}
	}
	
	// Finalize fields
	physDir, offDir := w.mapUncompToPhysical(node.dirUncompOffset)
	node.dirBlockStart = physDir
	node.dirBlockOffset = offDir
	
	data, err := w.serializeInode(node)
	if err != nil { return err }
	buf.Write(data)
	return nil
}

func (w *Writer) writeDirTable(node *writerInode, buf *bytes.Buffer) error {
	if node.fileType == DirType || node.fileType == XDirType {
		for _, child := range node.entries {
			if err := w.writeDirTable(child, buf); err != nil { return err }
		}
		
		// Write this dir's data
		if err := w.realWriteDirData(node, buf); err != nil { return err }
	}
	return nil
}

func (w *Writer) realWriteDirData(node *writerInode, buf *bytes.Buffer) error {
	if len(node.entries) == 0 {
		binary.Write(buf, binary.LittleEndian, uint32(0))
		binary.Write(buf, binary.LittleEndian, uint32(0))
		binary.Write(buf, binary.LittleEndian, node.ino)
		return nil
	}
	
	currentIndex := 0
	for currentIndex < len(node.entries) {
		baseRef := w.mapUncompToRef(node.entries[currentIndex].inodeUncompOffset)
		baseBlock := baseRef.Index()
		headerBaseIno := node.entries[currentIndex].ino
		
		endIndex := currentIndex
		for endIndex < len(node.entries) {
			ref := w.mapUncompToRef(node.entries[endIndex].inodeUncompOffset)
			if ref.Index() != baseBlock { break }
			if endIndex - currentIndex >= 256 { break }
			diff := int32(node.entries[endIndex].ino) - int32(headerBaseIno)
			if diff > 32767 || diff < -32768 { break }
			endIndex++
		}
		
		binary.Write(buf, binary.LittleEndian, uint32(endIndex-currentIndex-1))
		binary.Write(buf, binary.LittleEndian, uint32(baseBlock))
		binary.Write(buf, binary.LittleEndian, int32(headerBaseIno))
		
		for i := currentIndex; i < endIndex; i++ {
			child := node.entries[i]
			ref := w.mapUncompToRef(child.inodeUncompOffset)
			binary.Write(buf, binary.LittleEndian, uint16(ref.Offset()))
			binary.Write(buf, binary.LittleEndian, int16(int32(child.ino) - int32(headerBaseIno)))
			binary.Write(buf, binary.LittleEndian, child.fileType)
			binary.Write(buf, binary.LittleEndian, uint16(len(child.name)-1))
			buf.Write([]byte(child.name))
		}
		
		currentIndex = endIndex
	}
	return nil
}

// ... helper funcs ...
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
	// Metadata block for ID table
	blockStart := w.offset
	header := make([]byte, 2)
	binary.LittleEndian.PutUint16(header, uint16(len(idData))|0x8000)
	w.write(header)
	w.write(idData)
	
	w.idTableStart = w.offset
	pointer := make([]byte, 8)
	binary.LittleEndian.PutUint64(pointer, blockStart)
	return w.write(pointer)
}

func (w *Writer) writeFileData() error {
	var paths []string
	for p := range w.inodeMap { paths = append(paths, p) }
	sort.Strings(paths)
	
	for _, p := range paths {
		inode := w.inodeMap[p]
		if inode.fileType != FileType || inode.size == 0 || inode.srcFS == nil { continue }
		
		data, err := fs.ReadFile(inode.srcFS, inode.path)
		if err != nil { return err }
		
		inode.startBlock = w.offset
		// Just write uncompressed for simplicity/speed in this fix
		// Or compress if we want
		
		// Let's compress file data, it's independent
		blockSize := int(w.blockSize)
		inode.dataBlocks = make([]uint32, 0)
		for offset := 0; offset < len(data); offset += blockSize {
			end := offset + blockSize
			if end > len(data) { end = len(data) }
			block := data[offset:end]
			
			compressed, _ := w.comp.compress(block)
			if compressed != nil && len(compressed) < len(block) {
				w.write(compressed)
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(compressed)))
			} else {
				w.write(block)
				inode.dataBlocks = append(inode.dataBlocks, uint32(len(block))|0x01000000)
			}
		}
	}
	return nil
}

func (w *Writer) serializeInode(ino *writerInode) ([]byte, error) {
	buf := &bytes.Buffer{}
	order := binary.LittleEndian
	
	if err := writeBinary(buf, order, ino.fileType); err != nil { return nil, err }
	if err := writeBinary(buf, order, uint16(ino.mode&0777)); err != nil { return nil, err }
	
	uidIdx := w.idTable[ino.uid]
	gidIdx := w.idTable[ino.gid]
	if err := writeBinary(buf, order, uint16(uidIdx)); err != nil { return nil, err }
	if err := writeBinary(buf, order, uint16(gidIdx)); err != nil { return nil, err }
	if err := writeBinary(buf, order, int32(ino.modTime)); err != nil { return nil, err }
	if err := writeBinary(buf, order, ino.ino); err != nil { return nil, err }
	
	switch ino.fileType {
	case DirType:
		if err := writeBinary(buf, order, ino.dirBlockStart); err != nil { return nil, err }
		if err := writeBinary(buf, order, ino.nlink); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint16(ino.size)); err != nil { return nil, err }
		if err := writeBinary(buf, order, ino.dirBlockOffset); err != nil { return nil, err }
		parentIno := uint32(1)
		if ino.parent != nil { parentIno = ino.parent.ino }
		if err := writeBinary(buf, order, parentIno); err != nil { return nil, err }
	case XDirType:
		if err := writeBinary(buf, order, ino.nlink); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(ino.size)); err != nil { return nil, err }
		if err := writeBinary(buf, order, ino.dirBlockStart); err != nil { return nil, err }
		parentIno := uint32(1)
		if ino.parent != nil { parentIno = ino.parent.ino }
		if err := writeBinary(buf, order, parentIno); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint16(len(ino.dirIndex))); err != nil { return nil, err }
		if err := writeBinary(buf, order, ino.dirBlockOffset); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(0xFFFFFFFF)); err != nil { return nil, err }
		for _, idx := range ino.dirIndex {
			if err := writeBinary(buf, order, idx.Index); err != nil { return nil, err }
			if err := writeBinary(buf, order, idx.Start); err != nil { return nil, err }
			if err := writeBinary(buf, order, uint32(len(idx.Name)-1)); err != nil { return nil, err }
			if err := writeBinary(buf, order, []byte(idx.Name)); err != nil { return nil, err }
		}
	case FileType:
		if err := writeBinary(buf, order, uint32(ino.startBlock)); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(0xFFFFFFFF)); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(0)); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(ino.size)); err != nil { return nil, err }
		for _, blockSize := range ino.dataBlocks {
			if err := writeBinary(buf, order, blockSize); err != nil { return nil, err }
		}
	case SymlinkType:
		if err := writeBinary(buf, order, ino.nlink); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(len(ino.symTarget))); err != nil { return nil, err }
		if err := writeBinary(buf, order, []byte(ino.symTarget)); err != nil { return nil, err }
	case CharDevType, BlockDevType:
		if err := writeBinary(buf, order, ino.nlink); err != nil { return nil, err }
		if err := writeBinary(buf, order, uint32(0)); err != nil { return nil, err }
	case FifoType, SocketType:
		if err := writeBinary(buf, order, ino.nlink); err != nil { return nil, err }
	default:
		return nil, fmt.Errorf("unsupported inode type %d", ino.fileType)
	}
	return buf.Bytes(), nil
}

func writeBinary(buf *bytes.Buffer, order binary.ByteOrder, data interface{}) error {
	return binary.Write(buf, order, data)
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