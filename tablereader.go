package squashfs

import (
	"fmt"
	"io"
)

// TODO add buf cache to allow multiple accesses to same block without re-reading
type tableReader struct {
	sb    *Superblock
	buf   []byte
	offt  int64
	tofft int64 // position of table block list (when blocks aren't one after another)
}

func (sb *Superblock) newInodeReader(ino inodeRef) (*tableReader, error) {
	return sb.newTableReader(int64(sb.InodeTableStart)+int64(ino.Index()), int(ino.Offset()))
}

func (sb *Superblock) newTableReader(base int64, start int) (*tableReader, error) {
	ir := &tableReader{
		sb:   sb,
		offt: base,
	}

	err := ir.readBlock()
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("failed to read initial block: %w", err)
	}

	if start != 0 {
		// need to cut offset
		ir.buf = ir.buf[start:]
	}

	return ir, nil
}

func (sb *Superblock) newIndirectTableReader(base int64, start int) (*tableReader, error) {
	ir := &tableReader{
		sb:    sb,
		tofft: base,
	}

	err := ir.readBlock()
	if err != nil {
		return nil, err
	}

	if start != 0 {
		// need to cut offset
		ir.buf = ir.buf[start:]
	}

	return ir, nil
}

func (i *tableReader) readBlock() error {
	if i.tofft != 0 {
		// tofft mode
		buf := make([]byte, 8)
		_, err := i.sb.fs.ReadAt(buf, i.tofft)
		if err != nil {
			return err
		}
		i.offt = int64(i.sb.order.Uint64(buf))
	}
	buf := make([]byte, 2)
	_, err := i.sb.fs.ReadAt(buf, i.offt)
	if err != nil {
		return err
	}
	lenN := i.sb.order.Uint16(buf)
	nocompressFlag := false

	if lenN&0x8000 == 0x8000 {
		// not compressed
		nocompressFlag = true
		lenN = lenN & 0x7fff
	}

	buf = make([]byte, int(lenN))

	// read data
	_, err = i.sb.fs.ReadAt(buf, i.offt+2)
	if err != nil {
		return err
	}
	i.offt += int64(lenN) + 2
	if !nocompressFlag {
		// decompress
		buf, err = i.sb.Comp.decompress(buf)
		if err != nil {
			//log.Printf("squashfs: failed to read compressed data: %s", err)
			return err
		}
	}

	i.buf = buf

	return nil
}

func (i *tableReader) Read(p []byte) (int, error) {
	// read from buf, if empty call readBlock()
	if i.buf == nil {
		err := i.readBlock()
		if err != nil {
			return 0, err
		}
	}

	n := copy(p, i.buf)
	if n == len(i.buf) {
		i.buf = nil
	} else {
		i.buf = i.buf[n:]
	}

	return n, nil
}
