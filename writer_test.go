package squashfs_test

import (
	"bytes"
	"io/fs"
	"os"
	"testing"

	"github.com/KarpelesLab/squashfs"
)

func TestWriterBasic(t *testing.T) {
	var buf bytes.Buffer

	// Create a new writer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Add files using WalkDir
	err = fs.WalkDir(os.DirFS("testdata"), ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	// Finalize to write the filesystem
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	// Verify we wrote something
	if buf.Len() == 0 {
		t.Error("No data written")
	}

	// Verify the magic number
	data := buf.Bytes()
	if len(data) < 4 {
		t.Fatal("Output too small")
	}

	// Check for squashfs magic (little endian)
	if data[0] != 'h' || data[1] != 's' || data[2] != 'q' || data[3] != 's' {
		t.Errorf("Invalid magic number: %x %x %x %x", data[0], data[1], data[2], data[3])
	}

	t.Logf("Created SquashFS image of %d bytes", buf.Len())
}

func TestWriterWithOptions(t *testing.T) {
	var buf bytes.Buffer

	// Create writer with custom options
	w, err := squashfs.NewWriter(&buf,
		squashfs.WithBlockSize(65536),
		squashfs.WithCompression(squashfs.ZSTD),
	)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Finalize empty filesystem
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	if buf.Len() == 0 {
		t.Error("No data written")
	}
}

func TestWriterReadback(t *testing.T) {
	var buf bytes.Buffer

	// Create a simple filesystem
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Finalize to write the filesystem
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS image of %d bytes", buf.Len())

	// Try to read it back
	data := buf.Bytes()
	sqfs, err := squashfs.New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	t.Logf("Successfully read back SquashFS v%d.%d", sqfs.VMajor, sqfs.VMinor)
	t.Logf("Compression: %s, BlockSize: %d, InodeCnt: %d", sqfs.Comp, sqfs.BlockSize, sqfs.InodeCnt)
}

func TestWriterSetCompression(t *testing.T) {
	var buf bytes.Buffer

	// Create writer with default compression (GZip)
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Change compression to ZSTD
	w.SetCompression(squashfs.ZSTD)

	// Finalize to write the filesystem
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	// Read it back and verify compression type
	data := buf.Bytes()
	sqfs, err := squashfs.New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	if sqfs.Comp != squashfs.ZSTD {
		t.Errorf("Expected compression ZSTD, got %s", sqfs.Comp)
	}

	t.Logf("Successfully created SquashFS with %s compression", sqfs.Comp)
}
