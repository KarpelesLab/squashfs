package squashfs

import "log"

type inodeReader struct {
	sb   *Superblock
	buf  []byte
	offt int64
}

func (sb *Superblock) newInodeReader(ino inodeRef) (*inodeReader, error) {
	ir := &inodeReader{
		sb:   sb,
		offt: int64(sb.InodeTableStart + uint64(ino.Index())),
	}

	err := ir.readBlock()
	if err != nil {
		return nil, err
	}

	if offt := int(ino.Offset()); offt != 0 {
		// need to cut offset
		ir.buf = ir.buf[offt:]
	}

	return ir, nil
}

func (i *inodeReader) readBlock() error {
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
	if !nocompressFlag {
		// decompress
		buf, err = i.sb.Comp.decompress(buf)
		if err != nil {
			log.Printf("squashfs: failed to read compressed data: %s", err)
			return err
		}
	}

	i.buf = buf

	return nil
}

func (i *inodeReader) Read(p []byte) (int, error) {
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
