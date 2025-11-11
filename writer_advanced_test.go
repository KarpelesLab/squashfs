package squashfs_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/KarpelesLab/squashfs"
)

func TestWriterWithSubdirectories(t *testing.T) {
	// Create a test filesystem with subdirectories
	testFS := fstest.MapFS{
		"file1.txt":              {Data: []byte("hello world")},
		"dir1/file2.txt":         {Data: []byte("file in dir1")},
		"dir1/file3.txt":         {Data: []byte("another file in dir1")},
		"dir1/subdir/file4.txt":  {Data: []byte("file in subdir")},
		"dir2/file5.txt":         {Data: []byte("file in dir2")},
		"empty_dir/.placeholder": {Data: []byte("")}, // Use placeholder for empty dir
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Set source filesystem for reading file data
	w.SetSourceFS(testFS)

	// Add all files from testFS
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	// Finalize
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with subdirectories: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify directory structure
	entries, err := sqfs.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read root directory: %s", err)
	}
	t.Logf("Root directory has %d entries", len(entries))

	// Check that we can read dir1
	entries, err = sqfs.ReadDir("dir1")
	if err != nil {
		t.Fatalf("Failed to read dir1: %s", err)
	}
	if len(entries) < 2 {
		t.Errorf("Expected at least 2 entries in dir1, got %d", len(entries))
	}

	// Check that we can read file content
	data, err := fs.ReadFile(sqfs, "file1.txt")
	if err != nil {
		t.Fatalf("Failed to read file1.txt: %s", err)
	}
	if string(data) != "hello world" {
		t.Errorf("Expected 'hello world', got '%s'", string(data))
	}

	// Check nested file
	data, err = fs.ReadFile(sqfs, "dir1/subdir/file4.txt")
	if err != nil {
		t.Fatalf("Failed to read dir1/subdir/file4.txt: %s", err)
	}
	if string(data) != "file in subdir" {
		t.Errorf("Expected 'file in subdir', got '%s'", string(data))
	}
}

func TestWriterWithLargeDirectory(t *testing.T) {
	// Create a directory with many files to test indexing
	testFS := make(fstest.MapFS)

	// Add 1000 files to trigger directory indexing
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		testFS[name] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("content of file %d", i)),
		}
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Set source filesystem
	w.SetSourceFS(testFS)

	// Add all files
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	// Finalize
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with large directory: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify we can read the directory
	entries, err := sqfs.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read root directory: %s", err)
	}
	if len(entries) != 1000 {
		t.Errorf("Expected 1000 entries, got %d", len(entries))
	}

	// Verify we can read a file in the middle
	data, err := fs.ReadFile(sqfs, "file_0500.txt")
	if err != nil {
		t.Fatalf("Failed to read file_0500.txt: %s", err)
	}
	if string(data) != "content of file 500" {
		t.Errorf("Expected 'content of file 500', got '%s'", string(data))
	}

	// Verify we can read first and last files
	_, err = fs.ReadFile(sqfs, "file_0000.txt")
	if err != nil {
		t.Fatalf("Failed to read file_0000.txt: %s", err)
	}

	_, err = fs.ReadFile(sqfs, "file_0999.txt")
	if err != nil {
		t.Fatalf("Failed to read file_0999.txt: %s", err)
	}
}

func TestWriterWithNestedDirectories(t *testing.T) {
	// Create deeply nested directory structure
	testFS := make(fstest.MapFS)

	// Create a deep hierarchy: level0/level1/level2/.../level9/file.txt
	path := ""
	for i := 0; i < 10; i++ {
		if i > 0 {
			path += "/"
		}
		path += fmt.Sprintf("level%d", i)
	}
	testFS[path+"/deep_file.txt"] = &fstest.MapFile{
		Data: []byte("deeply nested content"),
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Set source filesystem
	w.SetSourceFS(testFS)

	// Add all files
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	// Finalize
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with nested directories: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify we can read the deeply nested file
	data, err := fs.ReadFile(sqfs, path+"/deep_file.txt")
	if err != nil {
		t.Fatalf("Failed to read deep_file.txt: %s", err)
	}
	if string(data) != "deeply nested content" {
		t.Errorf("Expected 'deeply nested content', got '%s'", string(data))
	}

	// Verify intermediate directories exist
	for i := 0; i < 10; i++ {
		dirPath := strings.Join(strings.Split(path, "/")[:i+1], "/")
		entries, err := sqfs.ReadDir(dirPath)
		if err != nil {
			t.Fatalf("Failed to read directory %s: %s", dirPath, err)
		}
		if len(entries) == 0 {
			t.Errorf("Directory %s is empty", dirPath)
		}
	}
}

func TestWriterMixedContent(t *testing.T) {
	// Create a mixed filesystem with various file sizes
	testFS := make(fstest.MapFS)

	// Empty file
	testFS["empty.txt"] = &fstest.MapFile{Data: []byte{}}

	// Small file
	testFS["small.txt"] = &fstest.MapFile{Data: []byte("x")}

	// Medium file
	testFS["medium.txt"] = &fstest.MapFile{Data: bytes.Repeat([]byte("medium content\n"), 100)}

	// Large file (over 1MB to test block handling)
	testFS["large.txt"] = &fstest.MapFile{Data: bytes.Repeat([]byte("large content\n"), 80000)}

	// Directory with files
	testFS["data/file1.dat"] = &fstest.MapFile{Data: []byte("data1")}
	testFS["data/file2.dat"] = &fstest.MapFile{Data: []byte("data2")}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	// Set source filesystem
	w.SetSourceFS(testFS)

	// Add all files
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	// Finalize
	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with mixed content: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify empty file
	data, err := fs.ReadFile(sqfs, "empty.txt")
	if err != nil {
		t.Fatalf("Failed to read empty.txt: %s", err)
	}
	if len(data) != 0 {
		t.Errorf("Expected empty file, got %d bytes", len(data))
	}

	// Verify large file
	data, err = fs.ReadFile(sqfs, "large.txt")
	if err != nil {
		t.Fatalf("Failed to read large.txt: %s", err)
	}
	expectedSize := len(bytes.Repeat([]byte("large content\n"), 80000))
	if len(data) != expectedSize {
		t.Errorf("Expected %d bytes, got %d", expectedSize, len(data))
	}
}
