package squashfs

import "fmt"

type SquashComp uint16

const (
	GZip SquashComp = 1
	LZMA            = 2
	LZO             = 3
	XZ              = 4
	LZ4             = 5
	ZSTD            = 6
)

func (s SquashComp) String() string {
	switch s {
	case GZip:
		return "GZip"
	case LZMA:
		return "LZMA"
	case LZO:
		return "LZO"
	case XZ:
		return "XZ"
	case LZ4:
		return "LZ4"
	case ZSTD:
		return "ZSTD"
	}
	return fmt.Sprintf("SquashComp(%d)", s)
}
