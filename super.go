package squashfs

import (
	"encoding/binary"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
)

const SuperblockSize = 96

// Superblock is the main object representing a squashfs image, and exposes various information about
// the file. You can ignore most of these and use the object directly to access files/etc, or inspect
// various elements of the squashfs image.
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
	runtime.SetFinalizer(sb, clean)
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

// SetInodeOffset allows setting the inode offset used for interacting with fuse. This can be safely ignored if not using fuse
// or when mounting only a single squashfs via fuse.
func (s *Superblock) SetInodeOffset(offt uint64) {
	s.inoOfft = offt
}

// FindInode returns the inode for a given path. If followSymlink is false and
// a symlink is found in the path, it will be followed anyway. If however the
// target file is a symlink, then its inode will be returned.
func (s *Superblock) FindInode(name string, followSymlinks bool) (*Inode, error) {
	return s.FindInodeAt(s.rootIno, name, followSymlinks)
}

// FindInodeAt returns an inode for a path starting at a given different inode,
// and can be used to implement methods such as OpenAt, etc.
//
// This will not prevent going higher than the given inode (ie. using ".." will
// allow someone to access the inode's parent).
func (s *Superblock) FindInodeAt(cur *Inode, name string, followSymlinks bool) (*Inode, error) {
	// similar to lookup, but handles slashes in name and returns an inode
	parent := cur
	symlinkRedirects := 40 // maximum number of redirects before giving up

	for {
		if len(name) == 0 {
			// trailing slash?
			return cur, nil
		}
		pos := strings.IndexByte(name, '/')
		if pos == -1 {
			// no / - perform final lookup
			if !followSymlinks {
				return cur.lookupRelativeInode(name)
			}
			res, err := cur.lookupRelativeInode(name)
			if err != nil {
				return nil, err
			}
			if !res.Type.IsSymlink() {
				return res, nil
			}

			// need to perform symlink lookup, we are not done here
			if symlinkRedirects == 0 {
				return nil, ErrTooManySymlinks
			}
			symlinkRedirects -= 1
			sym, err := res.Readlink()
			if err != nil {
				return nil, err
			}
			// ensure symlink isn't empty and isn't absolute either
			if len(sym) == 0 || sym[0] == '/' {
				return nil, fs.ErrInvalid
			}
			// continue lookup from that point
			cur = parent
			name = string(sym)
			continue
		}
		if pos == 0 {
			// skip initial or subsequent /
			name = name[1:]
			continue
		}
		if !cur.IsDir() {
			return nil, ErrNotDirectory
		}
		t, err := cur.lookupRelativeInode(name[:pos])
		if err != nil {
			return nil, err
		}

		if t.Type.IsSymlink() {
			if symlinkRedirects == 0 {
				return nil, ErrTooManySymlinks
			}
			symlinkRedirects -= 1

			sym, err := t.Readlink()
			if err != nil {
				return nil, err
			}
			// ensure symlink isn't empty and isn't absolute either
			if len(sym) == 0 || sym[0] == '/' {
				return nil, fs.ErrInvalid
			}
			// prepend symlink to name & remove symlink
			// if symlink a=b and name=a/c it becomes b/c
			name = string(sym) + name[pos:] // no +1 to pos means we keep the / we had in name
			// do not update cur since lookup resumes from that point
			continue
		}
		// there still are further lookups, so this must be a directory
		if !t.IsDir() {
			return nil, ErrNotDirectory
		}

		// move forward
		parent = cur
		cur = t
		name = name[pos+1:]
	}
}

// Open returns a fs.File for a given path, which can be a different object depending
// if the file is a regular file or a directory.
func (sb *Superblock) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.FindInode(name, true)
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

	ino, err := sb.FindInode(name, true)
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

	ino, err := sb.FindInode(name, true)
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

	ino, err := sb.FindInode(name, true)
	if err != nil {
		return nil, err
	}

	return &fileinfo{name: path.Base(name), ino: ino}, nil
}

// Lstat will return stats for a given path inside the sqhashfs archive. If
// the target is a symbolic link, data on the link itself will be returned.
func (sb *Superblock) Lstat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrInvalid}
	}

	ino, err := sb.FindInode(name, false)
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
