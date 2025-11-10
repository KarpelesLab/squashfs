package squashfs

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// Compression represents the compression algorithm used in a SquashFS filesystem.
// Different compression methods can be used to optimize for size or decompression speed.
// By default, only GZip compression is supported. Additional compression formats can be
// enabled through build tags or manually registering decompressors.
type Compression uint16

const (
	GZip Compression = iota + 1 // GZip compression (zlib, always supported)
	LZMA                        // LZMA compression (currently not implemented via build tag)
	LZO                         // LZO compression (currently not implemented via build tag)
	XZ                          // XZ compression (enabled with "xz" build tag)
	LZ4                         // LZ4 compression (currently not implemented via build tag)
	ZSTD                        // Zstandard compression (enabled with "zstd" build tag)
)

type Decompressor func(buf []byte) ([]byte, error)
type Compressor func(buf []byte) ([]byte, error)

// CompHandler contains both compression and decompression functions for a compression method.
type CompHandler struct {
	Decompress Decompressor
	Compress   Compressor
}

var compHandlers = map[Compression]*CompHandler{
	GZip: {
		Decompress: MakeDecompressorErr(zlib.NewReader),
		Compress:   zlibCompress,
	},
}

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
	if h, ok := compHandlers[s]; ok && h.Decompress != nil {
		return h.Decompress(buf)
	}
	return nil, fmt.Errorf("unsupported compression format %s", s.String())
}

func (s Compression) compress(buf []byte) ([]byte, error) {
	if h, ok := compHandlers[s]; ok && h.Compress != nil {
		return h.Compress(buf)
	}
	return nil, fmt.Errorf("unsupported compression format %s", s.String())
}

// zlibCompress compresses data using zlib (GZip compression)
func zlibCompress(buf []byte) ([]byte, error) {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	if _, err := w.Write(buf); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Register can be used to register compression handlers for squashfs.
// Pass nil for either decomp or comp to only register one direction.
// By default GZip is supported for both compression and decompression.
//
// Example usage:
//   - Register(ZSTD, decompressor, nil)     // read-only support
//   - Register(ZSTD, nil, compressor)       // write-only support
//   - Register(ZSTD, decompressor, compressor) // full support
func Register(method Compression, decomp Decompressor, comp Compressor) {
	if compHandlers[method] == nil {
		compHandlers[method] = &CompHandler{}
	}
	if decomp != nil {
		compHandlers[method].Decompress = decomp
	}
	if comp != nil {
		compHandlers[method].Compress = comp
	}
}

// RegisterDecompressor can be used to register a decompressor for squashfs.
// This is a convenience wrapper around Register for backward compatibility.
func RegisterDecompressor(method Compression, dcomp Decompressor) {
	Register(method, dcomp, nil)
}

// RegisterCompressor can be used to register a compressor for writing squashfs.
// This is a convenience wrapper around Register for backward compatibility.
func RegisterCompressor(method Compression, comp Compressor) {
	Register(method, nil, comp)
}

// RegisterCompHandler can be used to register both compressor and decompressor
// for a compression method at once by providing a CompHandler struct.
func RegisterCompHandler(method Compression, handler *CompHandler) {
	compHandlers[method] = handler
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
