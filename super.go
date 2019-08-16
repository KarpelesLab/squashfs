package squashfs

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"reflect"
	"sync"

	"git.atonline.com/azusa/apkg/apkgfs"
)

const SuperblockSize = 96

// https://dr-emann.github.io/squashfs/
type Superblock struct {
	fs    io.ReaderAt
	order binary.ByteOrder

	rootIno  *Inode
	rootInoN uint64
	inoIdx   map[uint32]inodeRef // inode refs (see export table)
	inoIdxL  sync.RWMutex
	inoOfft  uint64
	fuse     *apkgfs.PkgFS

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
	RootInode         inodeRef
	BytesUsed         uint64
	IdTableStart      uint64
	XattrIdTableStart uint64
	InodeTableStart   uint64
	DirTableStart     uint64
	FragTableStart    uint64
	ExportTableStart  uint64
}

func New(fs io.ReaderAt, inoOfft uint64, fuse *apkgfs.PkgFS) (*Superblock, error) {
	sb := &Superblock{fs: fs,
		fuse:    fuse,
		inoOfft: inoOfft,
		inoIdx:  make(map[uint32]inodeRef),
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
		return nil, errors.New("not squashfs 4.0")
	}

	if !sb.Flags.Has(EXPORTABLE) {
		return nil, errors.New("need exportable squashfs")
	}

	// get root inode
	sb.rootIno, err = sb.GetInodeRef(sb.RootInode)
	if err != nil {
		return nil, err
	}

	sb.rootInoN = uint64(sb.rootIno.Ino)

	return sb, nil
}

func (s *Superblock) UnmarshalBinary(data []byte) error {
	switch string(data[:4]) {
	case "hsqs":
		s.order = binary.LittleEndian
	case "sqsh":
		s.order = binary.BigEndian
	default:
		return errors.New("invalid squashfs partition")
	}

	s.Magic = s.order.Uint32(data[0:4])
	s.InodeCnt = s.order.Uint32(data[4:8])
	s.ModTime = int32(s.order.Uint32(data[8:12]))
	s.BlockSize = s.order.Uint32(data[12:16])
	s.FragCount = s.order.Uint32(data[16:20])
	s.Comp = SquashComp(s.order.Uint16(data[20:22]))
	s.BlockLog = s.order.Uint16(data[22:24])
	s.Flags = SquashFlags(s.order.Uint16(data[24:26]))
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
		return errors.New("invalid squashfs partition")
	}

	if uint32(1)<<s.BlockLog != s.BlockSize {
		return errors.New("invalid squashfs: block size check failed")
	}

	log.Printf("parsed SquashFS %d.%d blocksize=%d bytes=%d comp=%s flags=%s", s.VMajor, s.VMinor, s.BlockSize, s.BytesUsed, s.Comp, s.Flags)
	//log.Printf("inode table at 0x%x, export at 0x%x, count=%d, root=%s", s.InodeTableStart, s.ExportTableStart, s.InodeCnt, s.RootInode)

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
