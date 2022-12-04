//go:build xz

package squashfs

import (
	"io"

	"github.com/ulikunitz/xz"
)

func init() {
	RegisterDecompressor(XZ, MakeDecompressorErr(func(r io.Reader) (io.ReadCloser, error) {
		rc, err := xz.NewReader(r)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(rc), nil
	}))
}
