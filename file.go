package squashfs

import (
	"io"
	"io/fs"
	"path"
	"time"
)

// File is a convience object allowing using an inode as if it was a regular file
type File struct {
	*io.SectionReader
	ino  *Inode
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

// OpenFile returns a fs.File for a given inode. If the file is a directory, the returned object will implement
// fs.ReadDirFile. If it is a regular file it will also implement io.Seeker.
func (ino *Inode) OpenFile(name string) fs.File {
	switch ino.Type {
	case 1, 8:
		return &FileDir{ino: ino, name: name}
	default:
		sec := io.NewSectionReader(ino, 0, int64(ino.Size))
		return &File{SectionReader: sec, ino: ino, name: name}
	}
}

// (File)

// Stat returns the details of the open file
func (f *File) Stat() (fs.FileInfo, error) {
	return &fileinfo{name: path.Base(f.name), ino: f.ino}, nil
}

// Sys returns a *Inode object for this file
func (f *File) Sys() any {
	return f.ino
}

// Close actually does nothing and exists to comply with fs.File
func (f *File) Close() error {
	return nil
}

// (FileDir)

// Read on a directory is invalid and will always fail
func (d *FileDir) Read(p []byte) (int, error) {
	return 0, fs.ErrInvalid
}

// Stat returns details on the file
func (d *FileDir) Stat() (fs.FileInfo, error) {
	return &fileinfo{name: path.Base(d.name), ino: d.ino}, nil
}

// Sys returns a *inode object for this file, similar to calling Stat().Sys()
func (d *FileDir) Sys() any {
	return d.ino
}

// Close resets the dir reader
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

// Name returns the file's base name
func (fi *fileinfo) Name() string {
	return fi.name
}

// Size returns the file's size
func (fi *fileinfo) Size() int64 {
	return int64(fi.ino.Size)
}

// Mode returns the file's mode
func (fi *fileinfo) Mode() fs.FileMode {
	return fi.ino.Mode()
}

// ModTime returns the file's latest modified time. Note that squashfs stores
// this as a int32, which means it'll stop working after 2038.
func (fi *fileinfo) ModTime() time.Time {
	return time.Unix(int64(fi.ino.ModTime), 0)
}

// IsDir returns true if this is a directory
func (fi *fileinfo) IsDir() bool {
	return fi.ino.IsDir()
}

// Sys returns the *Inode object matching this file
func (fi *fileinfo) Sys() any {
	return fi.ino
}
