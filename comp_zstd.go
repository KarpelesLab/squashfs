//go:build zstd

package squashfs

import (
	"bytes"

	"github.com/klauspost/compress/zstd"
)

func zstdCompress(buf []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := zstd.NewWriter(&out)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(buf); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func init() {
	RegisterCompHandler(ZSTD, &CompHandler{
		Decompress: MakeDecompressor(zstd.ZipDecompressor()),
		Compress:   zstdCompress,
	})
}
