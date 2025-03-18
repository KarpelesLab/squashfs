package squashfs

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// Compression represents the compression algorithm used in a SquashFS filesystem.
// Different compression methods can be used to optimize for size or decompression speed.
// By default, only GZip compression is supported. Use build tags and RegisterDecompressor
// to enable support for other compression formats.
type Compression uint16

const (
	GZip Compression = iota + 1 // GZip compression (zlib, always supported)
	LZMA                        // LZMA compression (requires lzma build tag)
	LZO                         // LZO compression (requires lzo build tag)
	XZ                          // XZ compression (requires xz build tag)
	LZ4                         // LZ4 compression (requires lz4 build tag)
	ZSTD                        // Zstandard compression (requires zstd build tag)
)

type Decompressor func(buf []byte) ([]byte, error)

var decompressHandler = map[Compression]Decompressor{GZip: MakeDecompressorErr(zlib.NewReader)}

func (s Compression) String() string {
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
	return fmt.Sprintf("Compression(%d)", s)
}

func (s Compression) decompress(buf []byte) ([]byte, error) {
	if f, ok := decompressHandler[s]; ok {
		return f(buf)
	}
	return nil, fmt.Errorf("unsupported compression format %s", s.String())
}

// RegisterDecompressor can be used to register a decompressor for squashfs.
// By default GZip is supported. The method shall take a buffer and return a
// decompressed buffer.
func RegisterDecompressor(method Compression, dcomp Decompressor) {
	decompressHandler[method] = dcomp
}

// MakeDecompressor allows using a decompressor made for archive/zip with
// SquashFs. It has some overhead as instead of simply dealing with buffer this
// uses the reader/writer API, but should allow to easily handle some formats.
//
// Example use:
// * squashfs.RegisterDecompressor(squashfs.ZSTD, squashfs.MakeDecompressor(zstd.ZipDecompressor()))
// * squashfs.RegisterDecompressor(squashfs.LZ4, squashfs.MakeDecompressor(lz4.NewReader)))
func MakeDecompressor(dec func(r io.Reader) io.ReadCloser) Decompressor {
	return func(buf []byte) ([]byte, error) {
		r := bytes.NewReader(buf)
		p := dec(r)
		defer p.Close()
		w := &bytes.Buffer{}
		_, err := io.Copy(w, p)
		return w.Bytes(), err
	}
}

// MakeDecompressorErr is similar to MakeDecompressor but the factory method also
// returns an error.
//
// Example use:
// * squashfs.RegisterDecompressor(squashfs.LZMA, squashfs.MakeDecompressorErr(lzma.NewReader))
// * squashfs.RegisterDecompressor(squashfs.XZ, squashfs.MakeDecompressorErr(xz.NewReader))
func MakeDecompressorErr(dec func(r io.Reader) (io.ReadCloser, error)) Decompressor {
	return func(buf []byte) ([]byte, error) {
		r := bytes.NewReader(buf)
		p, err := dec(r)
		if err != nil {
			return nil, err
		}
		defer p.Close()
		w := &bytes.Buffer{}
		_, err = io.Copy(w, p)
		return w.Bytes(), err
	}
}
