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
	// read next entry
	if dr.r.N == 3 {
		return "", 0, io.EOF // probably
	}

	var offset, typ, siz uint16
	var inoNum2 int16
	var name []byte

	if dr.count == 0 {
		err := dr.readHeader()
		if err != nil {
			return "", 0, err
		}
	}

	// read entry
	err := binary.Read(dr.r, dr.sb.order, &offset)
	if err != nil {
		return "", 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &inoNum2)
	if err != nil {
		return "", 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &typ)
	if err != nil {
		return "", 0, err
	}
	err = binary.Read(dr.r, dr.sb.order, &siz)
	if err != nil {
		return "", 0, err
	}
	name = make([]byte, int(siz)+1)
	_, err = io.ReadFull(dr.r, name)
	if err != nil {
		return "", 0, err
	}

	dr.count -= 1

	inoRef := inodeRef((uint64(dr.startBlock) << 16) | uint64(offset))
	return string(name), inoRef, nil
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
		ename, inoR, err := dr.next()
		if err != nil {
			if err == io.EOF {
				return res, nil
			}
			return res, err
		}

		// found
		found, err := dr.sb.GetInodeRef(inoR)
		if err != nil {
			return res, err
		}
		// cache
		dr.sb.inoIdxL.Lock()
		dr.sb.inoIdx[found.Ino] = inoR
		dr.sb.inoIdxL.Unlock()
		// append
		res = append(res, fs.FileInfoToDirEntry(&fileinfo{name: ename, ino: found}))

		if n > 0 && len(res) >= n {
			return res, nil
		}
	}
}
