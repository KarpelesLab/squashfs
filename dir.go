package squashfs

import (
	"encoding/binary"
	"io"
	"io/fs"
)

// dirReader provides sequential access to entries in a SquashFS directory.
// It handles reading directory headers and entries, providing a stream of directory entries.
type dirReader struct {
	sb *Superblock       // parent superblock
	r  *io.LimitedReader // reader for directory data

	count, startBlock, inodeNum uint32 // directory entry metadata
}

// direntry implements fs.DirEntry interface for SquashFS directory entries.
// It provides information about a single file or directory within a directory.
type direntry struct {
	name string      // file name
	typ  Type        // file type (regular, directory, symlink, etc.)
	inoR inodeRef    // reference to the inode
	sb   *Superblock // parent superblock
}

// DirIndexEntry represents an entry in a directory index.
// Directory indices allow fast lookups in large directories by providing
// a sorted index of filenames and their positions in the directory data.
type DirIndexEntry struct {
	Index uint32 // position in the directory data
	Start uint32 // block offset
	Name  string // filename at this index point
}

func (sb *Superblock) dirReader(i *Inode, seek *DirIndexEntry) (*dirReader, error) {
	if seek != nil {
		tbl, err := i.sb.newTableReader(int64(i.sb.DirTableStart)+int64(seek.Start), (int(i.Offset)+int(seek.Index))&0x1fff)
		if err != nil {
			return nil, err
		}
		dr := &dirReader{
			sb: i.sb,
			r:  &io.LimitedReader{R: tbl, N: int64(i.Size) - int64(seek.Index)},
		}
		return dr, nil
	}

	tbl, err := i.sb.newTableReader(int64(i.sb.DirTableStart)+int64(i.StartBlock), int(i.Offset))
	if err != nil {
		return nil, err
	}

	dr := &dirReader{
		sb: i.sb,
		r:  &io.LimitedReader{R: tbl, N: int64(i.Size)},
	}

	return dr, nil
}

func (dr *dirReader) next() (string, inodeRef, error) {
	name, _, inoR, err := dr.nextfull()
	return name, inoR, err
}

func (dr *dirReader) nextfull() (string, Type, inodeRef, error) {
	// read next entry
	if dr.r.N == 3 {
		return "", 0, 0, io.EOF // probably
	}

	var offset, siz uint16
	var typ Type
	var inoNum2 int16
	var name []byte

	if dr.count == 0 {
		err := dr.readHeader()
		if err != nil {
			return "", 0, 0, err
		}
	}

	// read entry
	err := binary.Read(dr.r, dr.sb.order, &offset)
	if err != nil {
		return "", 0, 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &inoNum2)
	if err != nil {
		return "", 0, 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &typ)
	if err != nil {
		return "", 0, 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &siz)
	if err != nil {
		return "", 0, 0, err
	}
	name = make([]byte, int(siz)+1)
	_, err = io.ReadFull(dr.r, name)
	if err != nil {
		return "", 0, 0, err
	}

	dr.count -= 1

	inoRef := inodeRef((uint64(dr.startBlock) << 16) | uint64(offset))
	return string(name), typ, inoRef, nil
}

func (dr *dirReader) readHeader() error {
	// read dir header
	err := binary.Read(dr.r, dr.sb.order, &dr.count)
	if err != nil {
		return err
	}
	err = binary.Read(dr.r, dr.sb.order, &dr.startBlock)
	if err != nil {
		return err
	}
	err = binary.Read(dr.r, dr.sb.order, &dr.inodeNum)
	if err != nil {
		return err
	}

	//log.Printf("read header, count=0x%x+1 startBlock=%x inodeNum=%x", dr.count, dr.startBlock, dr.inodeNum)
	dr.count += 1

	return nil
}

func (dr *dirReader) ReadDir(n int) ([]fs.DirEntry, error) {
	var res []fs.DirEntry

	for {
		ename, typ, inoR, err := dr.nextfull()
		if err != nil {
			if err == io.EOF {
				return res, nil
			}
			return res, err
		}

		res = append(res, &direntry{ename, typ, inoR, dr.sb})
		if n > 0 && len(res) >= n {
			return res, nil
		}
	}
}

func (de *direntry) Name() string {
	return de.name
}

func (de *direntry) IsDir() bool {
	switch de.typ {
	case 1, 8:
		return true
	default:
		return false
	}
}

func (de *direntry) Type() fs.FileMode {
	return de.typ.Mode()
}

func (de *direntry) Info() (fs.FileInfo, error) {
	// found
	found, err := de.sb.GetInodeRef(de.inoR)
	if err != nil {
		return nil, err
	}
	// cache
	de.sb.setInodeRefCache(found.Ino, de.inoR)
	// append
	return &fileinfo{name: de.name, ino: found}, nil
}
