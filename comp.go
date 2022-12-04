package squashfs

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

type SquashComp uint16

type Decompressor func(buf []byte) ([]byte, error)

var (
	decompressHandler = map[SquashComp]Decompressor{
		GZip: decompressGzip,
	}
)

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

// RegisterDecompressor can be used to register a decompressor for squashfs.
// By default GZip is supported. The method shall take a buffer and return a
// decompressed buffer.
func RegisterDecompressor(method SquashComp, dcomp Decompressor) {
	decompressHandler[method] = dcomp
}

func (s SquashComp) decompress(buf []byte) ([]byte, error) {
	if f, ok := decompressHandler[s]; ok {
		return f(buf)
	}
	return nil, fmt.Errorf("unsupported compression format %s", s.String())
}

func decompressGzip(buf []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	b := &bytes.Buffer{}
	_, err = io.Copy(b, r)
	if err != nil {
		return nil, err
	}
	r.Close()
	return b.Bytes(), nil
}

// MakeDecompressor allows using a decompressor made for archive/zip with
// SquashFs. It has some overhead as instead of simply dealing with buffer this
// uses the reader/writer API, but should allow to easily handle some formats.
//
// Example use:
// squashfs.RegisterDecompressor(squashfs.ZSTD, squashfs.MakeDecompressor(zstd.ZipDecompressor()))
func MakeDecompressor(dec func(r io.Reader) io.ReadCloser) func([]byte) ([]byte, error) {
	return func(buf []byte) ([]byte, error) {
		r := bytes.NewReader(buf)
		w := &bytes.Buffer{}
		p := dec(r)
		defer p.Close()
		_, err := io.Copy(w, p)
		return w.Bytes(), err
	}
}
