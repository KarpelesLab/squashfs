package squashfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"reflect"
)

// https://dr-emann.github.io/squashfs/
type Superblock struct {
	fs    io.ReaderAt
	order binary.ByteOrder

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
	head := make([]byte, sb.binarySize())

	log.Printf("squash: read header %d bytes", len(head))
	_, err := fs.ReadAt(head, 0)
	if err != nil {
		return nil, err
	}
	log.Printf("squash: read header, parsing")
	err = sb.UnmarshalBinary(head)
	if err != nil {
		return nil, err
	}

	return sb, nil
}

func (s *Superblock) UnmarshalBinary(data []byte) error {
	v := reflect.ValueOf(s).Elem()
	c := v.NumField()
	r := bytes.NewReader(data)

	switch string(data[:4]) {
	case "hsqs":
		s.order = binary.LittleEndian
	case "sqsh":
		s.order = binary.BigEndian
	default:
		return errors.New("invalid squashfs partition")
	}

	// Decode
	var err error
	for i := 0; i < c; i++ {
		c := v.Type().Field(i).Name[0]
		if c < 'A' || c > 'Z' {
			continue
		}
		log.Printf("read %s", v.Type().Field(i).Name)
		err = binary.Read(r, s.order, v.Field(i).Interface())
		if err != nil {
			return err
		}
	}

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
