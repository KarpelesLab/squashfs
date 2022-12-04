package squashfs

import (
	"context"
	"encoding/binary"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"runtime"
	"sync"
)

const SuperblockSize = 96

// https://dr-emann.github.io/squashfs/
type Superblock struct {
	fs    io.ReaderAt
	order binary.ByteOrder
	clos  io.Closer

	rootIno  *Inode
	rootInoN uint64
	inoIdx   map[uint32]inodeRef // inode refs cache (see export table)
	inoIdxL  sync.RWMutex
	inoOfft  uint64
	idTable  []uint32

	Magic             uint32 // magic identifier
	InodeCnt          uint32 // number of inodes in filesystem
	ModTime           int32  // creation unix time as int32 (will stop working in 2038)
	BlockSize         uint32 // size of a single data block, must match 1<<BlockLog
	FragCount         uint32
	Comp              Compression // Compression used, usually GZip
	BlockLog          uint16
	Flags             Flags // squashfs flags
	IdCount           uint16
	VMajor            uint16
	VMinor            uint16
	RootInode         inodeRef // inode number/reference of root
	BytesUsed         uint64
	IdTableStart      uint64
	XattrIdTableStart uint64
	InodeTableStart   uint64
	DirTableStart     uint64
	FragTableStart    uint64
	ExportTableStart  uint64
}

var _ fs.FS = (*Superblock)(nil)
var _ fs.ReadDirFS = (*Superblock)(nil)
var _ fs.StatFS = (*Superblock)(nil)

// New returns a new instance of superblock for a given io.ReaderAt that can
// be used to access files inside squashfs.
func New(fs io.ReaderAt, options ...Option) (*Superblock, error) {
	sb := &Superblock{fs: fs,
		inoIdx: make(map[uint32]inodeRef),
	}
	head := make([]byte, SuperblockSize)

	_, err := fs.ReadAt(head, 0)
	if err != nil {
		return nil, err
	}
	err = sb.UnmarshalBinary(head)
	if err != nil {
		return nil, err
	}

	if sb.VMajor != 4 || sb.VMinor != 0 {
		return nil, ErrInvalidVersion
	}

	// apply options
	for _, opt := range options {
		err = opt(sb)
		if err != nil {
			return nil, err
		}
	}

	// get root inode
	sb.rootIno, err = sb.GetInodeRef(sb.RootInode)
	if err != nil {
		return nil, err
	}

	sb.rootInoN = uint64(sb.rootIno.Ino)

	sb.readIdTable()

	return sb, nil
}

// Open returns a new instance of superblock for a given file that can
// be used to access files inside squashfs. The file will be closed by
// the garbage collector or when Close() is called on the superblock.
func Open(file string, options ...Option) (*Superblock, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	sb, err := New(f, options...)
	if err != nil {
		f.Close()
		return nil, err
	}
	sb.clos = f

	clean := func(sb *Superblock) {
		sb.Close()
	}
	runtime.SetFinalizer(f, clean)
	return sb, nil
}

func (sb *Superblock) readIdTable() error {
	// read id table
	idtable, err := sb.newIndirectTableReader(int64(sb.IdTableStart), 0)
	if err != nil {
		return err
	}
	var id uint32
	sb.idTable = make([]uint32, sb.IdCount)
	for i := range sb.idTable {
		err := binary.Read(idtable, sb.order, &id)
		if err != nil {
			return err
		}
		sb.idTable[i] = id
	}
	//log.Printf("sqashfs: id table = %+v", sb.idTable)
	return nil
}

// UnmarshalBinary reads a binary header values into Superblock
func (s *Superblock) UnmarshalBinary(data []byte) error {
	if len(data) != SuperblockSize {
		return ErrInvalidSuper
	}

	switch string(data[:4]) {
	case "hsqs":
		s.order = binary.LittleEndian
	case "sqsh":
		s.order = binary.BigEndian
	default:
		return ErrInvalidFile
	}

	s.Magic = s.order.Uint32(data[0:4])
	s.InodeCnt = s.order.Uint32(data[4:8])
	s.ModTime = int32(s.order.Uint32(data[8:12]))
	s.BlockSize = s.order.Uint32(data[12:16])
	s.FragCount = s.order.Uint32(data[16:20])
	s.Comp = Compression(s.order.Uint16(data[20:22]))
	s.BlockLog = s.order.Uint16(data[22:24])
	s.Flags = Flags(s.order.Uint16(data[24:26]))
	s.IdCount = s.order.Uint16(data[26:28])
	s.VMajor = s.order.Uint16(data[28:30])
	s.VMinor = s.order.Uint16(data[30:32])
	s.RootInode = inodeRef(s.order.Uint64(data[32:40]))
	s.BytesUsed = s.order.Uint64(data[40:48])
	s.IdTableStart = s.order.Uint64(data[48:56])
	s.XattrIdTableStart = s.order.Uint64(data[56:64])
	s.InodeTableStart = s.order.Uint64(data[64:72])
	s.DirTableStart = s.order.Uint64(data[72:80])
	s.FragTableStart = s.order.Uint64(data[80:88])
	s.ExportTableStart = s.order.Uint64(data[88:96])

	if s.Magic != 0x73717368 {
		// shouldn't happen
		return ErrInvalidFile
	}

	if uint32(1)<<s.BlockLog != s.BlockSize {
		return ErrInvalidSuper
	}

	//log.Printf("parsed SquashFS %d.%d blocksize=%d bytes=%d comp=%s flags=%s", s.VMajor, s.VMinor, s.BlockSize, s.BytesUsed, s.Comp, s.Flags)
	//log.Printf("inode table at 0x%x, export at 0x%x, count=%d, root=%s", s.InodeTableStart, s.ExportTableStart, s.InodeCnt, s.RootInode)

	return nil
}

func (s *Superblock) binarySize() int {
	v := reflect.ValueOf(s).Elem()
	c := v.NumField()
	sz := uintptr(0)

	for i := 0; i < c; i++ {
		c := v.Type().Field(i).Name[0]
		if c < 'A' || c > 'Z' {
			continue
		}
		sz += v.Field(i).Type().Size()
	}
	return int(sz)
}

// SetInodeOffset allows setting the inode offset used for interacting with fuse. This can be safely ignored if not using fuse
// or when mounting only a single squashfs via fuse.
func (s *Superblock) SetInodeOffset(offt uint64) {
	s.inoOfft = offt
}

// Open returns a fs.File for a given path, which can be a different object depending
// if the file is a regular file or a directory.
func (sb *Superblock) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.rootIno.LookupRelativeInodePath(context.Background(), name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}

	return ino.OpenFile(path.Base(name)), nil
}

// Readlink allows reading the value of a symbolic link inside the archive.
func (sb *Superblock) Readlink(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.rootIno.LookupRelativeInodePath(context.Background(), name)
	if err != nil {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: err}
	}

	res, err := ino.Readlink()
	if err != nil {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: err}
	}
	return string(res), nil
}

// ReadDir implements fs.ReadDirFS and allows listing any directory inside the archive
func (sb *Superblock) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.rootIno.LookupRelativeInodePath(context.Background(), name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}

	switch ino.Type {
	case 1, 8:
		// basic dir, we need to iterate (cache data?)
		dr, err := sb.dirReader(ino)
		if err != nil {
			return nil, err
		}
		return dr.ReadDir(0)
	default:
		return nil, fs.ErrInvalid
	}
}

// Stat will return stats for a given path inside the squashfs archive
func (sb *Superblock) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.rootIno.LookupRelativeInodePath(context.Background(), name)
	if err != nil {
		return nil, err
	}

	return &fileinfo{name: path.Base(name), ino: ino}, nil
}

// Close will close the underlying file when a filesystem was open with Open()
func (sb *Superblock) Close() error {
	if sb.clos != nil {
		return sb.clos.Close()
	}
	return nil
}

func (sb *Superblock) getInodeRefCache(ino uint32) (inodeRef, bool) {
	sb.inoIdxL.RLock()
	defer sb.inoIdxL.RUnlock()
	res, ok := sb.inoIdx[ino]
	return res, ok
}

func (sb *Superblock) setInodeRefCache(ino uint32, inoR inodeRef) {
	sb.inoIdxL.Lock()
	defer sb.inoIdxL.Unlock()
	sb.inoIdx[ino] = inoR
}
