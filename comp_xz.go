//go:build xz

package squashfs

import (
	"bytes"
	"io"

	"github.com/ulikunitz/xz"
)

func xzCompress(buf []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(buf); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func init() {
	RegisterCompHandler(XZ, &CompHandler{
		Decompress: MakeDecompressorErr(func(r io.Reader) (io.ReadCloser, error) {
			rc, err := xz.NewReader(r)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(rc), nil
		}),
		Compress: xzCompress,
	})
}
