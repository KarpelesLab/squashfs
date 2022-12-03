package squashfs

import (
	"io"
	"io/fs"
	"time"
)

// File is a convience object allowing using an inode as if it was a regular file
type File struct {
	ino  *Inode
	pos  int64
	name string
}

// FileDir is a convenience object allowing using a dir inode as if it was a regular file
type FileDir struct {
	ino  *Inode
	name string
	r    *dirReader
}

type fileinfo struct {
	ino  *Inode
	name string
}

// Ensure File respects fs.File & others
var _ fs.File = (*File)(nil)
var _ io.ReaderAt = (*File)(nil)

var _ fs.ReadDirFile = (*FileDir)(nil)

var _ fs.FileInfo = (*fileinfo)(nil)

// OpenFile returns a fs.File for a given inode
func (ino *Inode) OpenFile(name string) fs.File {
	switch ino.Type {
	case 1, 8:
		return &FileDir{ino: ino, name: name}
	default:
		return &File{ino: ino, name: name}
	}
}

// (File)

// ReadAt reads from the given file at a given position implementing os.ReaderAt
func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	return f.ino.ReadAt(p, off)
}

func (f *File) Read(p []byte) (int, error) {
	n, err := f.ino.ReadAt(p, f.pos)
	if n > 0 {
		f.pos += int64(n)
	}
	return n, err
}

func (f *File) Stat() (fs.FileInfo, error) {
	return &fileinfo{
		name: f.name,
		ino:  f.ino,
	}, nil
}

func (f *File) Close() error {
	return nil
}

// (FileDir)

func (d *FileDir) Read(p []byte) (int, error) {
	return 0, fs.ErrInvalid
}

func (d *FileDir) Stat() (fs.FileInfo, error) {
	return &fileinfo{name: d.name, ino: d.ino}, nil
}

func (d *FileDir) Close() error {
	d.r = nil
	return nil
}

func (d *FileDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.r == nil {
		dr, err := d.ino.sb.dirReader(d.ino)
		if err != nil {
			return nil, err
		}
		d.r = dr
	}

	return d.r.ReadDir(n)
}

// (fileinfo)

func (fi *fileinfo) Name() string {
	return fi.name
}

func (fi *fileinfo) Size() int64 {
	return int64(fi.ino.Size)
}

func (fi *fileinfo) Mode() fs.FileMode {
	return fi.ino.Mode()
}

func (fi *fileinfo) ModTime() time.Time {
	return time.Unix(int64(fi.ino.ModTime), 0)
}

func (fi *fileinfo) IsDir() bool {
	switch fi.ino.Type {
	case 1, 8:
		return true
	default:
		return false
	}
}

func (fi *fileinfo) Sys() any {
	return fi.ino
}
