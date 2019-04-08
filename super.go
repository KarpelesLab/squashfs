package squashfs

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"
)

// https://dr-emann.github.io/squashfs/
type Superblock struct {
	fs io.ReaderAt

	Magic             uint32
	InodeCnt          uint32
	ModTime           int32
	BlockSize         uint32
	FragCount         uint32
	Comp              SquashComp
	BlockLog          uint16
	Flags             SquashFlags
	IdCount           uint16
	VMajor            uint16
	VMinor            uint16
	RootInode         uint64
	BytesUsed         uint64
	IdTableStart      uint64
	XattrIdTableStart uint64
	InodeTableStart   uint64
	DirTableStart     uint64
	FragTableStart    uint64
	ExportTableStart  uint64
}

func New(fs io.ReaderAt) (*Superblock, error) {
	sb := &Superblock{fs: fs}
}

func (s *Superblock) UnmarshalBinary(data []byte) error {
	v := reflect.ValueOf(s)
	c := v.NumField()
	r := bytes.NewReader(data)

	// Decode
	var err error
	for i := 0; i < c; i++ {
		err = binary.Read(r, order, v.Field(i).Interface())
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Superblock) binarySize() int {
	v := reflect.ValueOf(s)
	c := v.NumField()
	s := 0

	for i := 0; i < c; i++ {
		s += v.Field(i).Type().Size()
	}
	return s
}
