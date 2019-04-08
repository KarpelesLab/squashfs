package squashfs

type SquashComp uint16

const (
	GZip SquashComp = 1
	LZMA            = 2
	LZO             = 3
	XZ              = 4
	LZ4             = 5
	ZSTD            = 6
)
