package squashfs

import (
	"encoding/binary"
	"io"
	"io/fs"
)

type dirReader struct {
	sb *Superblock
	r  *io.LimitedReader

	count, startBlock, inodeNum uint32
}

type direntry struct {
	name string
	typ  uint16 // squashfs type
	inoR inodeRef
	sb   *Superblock
}

func (sb *Superblock) dirReader(i *Inode) (*dirReader, error) {
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

func (dr *dirReader) nextfull() (string, uint16, inodeRef, error) {
	// read next entry
	if dr.r.N == 3 {
		return "", 0, 0, io.EOF // probably
	}

	var offset, typ, siz uint16
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
	return squashfsTypeToMode(de.typ)
}

func (de *direntry) Info() (fs.FileInfo, error) {
	// found
	found, err := de.sb.GetInodeRef(de.inoR)
	if err != nil {
		return nil, err
	}
	// cache
	de.sb.inoIdxL.Lock()
	de.sb.inoIdx[found.Ino] = de.inoR
	de.sb.inoIdxL.Unlock()
	// append
	return &fileinfo{name: de.name, ino: found}, nil
}
