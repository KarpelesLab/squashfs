//go:build zstd

package squashfs

import "github.com/klauspost/compress/zstd"

func init() {
	RegisterDecompressor(ZSTD, MakeDecompressor(zstd.ZipDecompressor()))
}
