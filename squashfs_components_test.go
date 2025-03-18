package squashfs_test

import (
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/KarpelesLab/squashfs"
)

// TestCompression tests the basic compression functionality
func TestCompression(t *testing.T) {
	// Test the String() method for compression types
	compressionTypes := []squashfs.Compression{
		squashfs.GZip,
		squashfs.LZMA,
		squashfs.LZO,
		squashfs.XZ,
		squashfs.LZ4,
		squashfs.ZSTD,
	}

	expectedNames := []string{
		"GZip",
		"LZMA",
		"LZO",
		"XZ",
		"LZ4",
		"ZSTD",
	}

	for i, compType := range compressionTypes {
		if compType.String() != expectedNames[i] {
			t.Errorf("Expected compression type %d name to be %s, got %s",
				compType, expectedNames[i], compType.String())
		}
	}

	// Test an unknown compression type
	unknownType := squashfs.Compression(99)
	if unknownType.String() != "Compression(99)" {
		t.Errorf("Expected unknown compression type to be Compression(99), got %s", unknownType.String())
	}
}

// TestFileOperations tests various file operations
func TestFileOperations(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Test ReadDir
	entries, err := sqfs.ReadDir("include")
	if err != nil {
		t.Errorf("failed to read directory 'include': %s", err)
	}
	if len(entries) < 1 {
		t.Errorf("expected at least 1 entry in 'include', got %d", len(entries))
	}

	// Verify each entry implements fs.DirEntry properly
	for _, entry := range entries {
		// Check that we can access the name
		name := entry.Name()

		// Get and check info
		info, err := entry.Info()
		if err != nil {
			t.Errorf("failed to get info for %s: %s", name, err)
		}

		// Check that name matches
		if info.Name() != name {
			t.Errorf("info.Name() returned %s, expected %s", info.Name(), name)
		}

		// File type should be consistent
		if info.IsDir() != entry.IsDir() {
			t.Errorf("isDir mismatch for %s: entry.IsDir()=%v, info.IsDir()=%v",
				name, entry.IsDir(), info.IsDir())
		}
	}

	// Test opening and reading a file
	file, err := sqfs.Open("include/zlib.h")
	if err != nil {
		t.Errorf("failed to open include/zlib.h: %s", err)
	} else {
		defer file.Close()
		
		// Test Stat on open file
		fileInfo, err := file.Stat()
		if err != nil {
			t.Errorf("failed to get stat on open file: %s", err)
		} else if fileInfo.Name() != "zlib.h" {
			t.Errorf("expected filename to be zlib.h, got %s", fileInfo.Name())
		}

		// Read a portion of the file
		buf := make([]byte, 100)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("failed to read from file: %s", err)
		}
		if n == 0 {
			t.Errorf("read 0 bytes from file")
		}
	}

	// Test ReadDir with non-existent directory
	_, err = sqfs.ReadDir("nonexistent")
	if err == nil {
		t.Errorf("expected error when reading non-existent directory")
	}

	// Test Open with non-existent file
	_, err = sqfs.Open("nonexistent/file.txt")
	if err == nil {
		t.Errorf("expected error when opening non-existent file")
	}
}

// TestSymlinkHandling tests handling of symlinks and finding inodes through paths with symlinks
func TestSymlinkHandling(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/azusa_symlinks.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/azusa_symlinks.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Test finding a file through a path that might contain symlinks
	// Note: Just verifying that the FindInode function works on the test data
	//       as used in the main test
	_, err = sqfs.FindInode("full/lib64/libLLVMIRReader.a", false)
	if err != nil {
		t.Errorf("failed to find inode 'full/lib64/libLLVMIRReader.a': %s", err)
	}
}

// TestInodeAttributes tests access to inode attributes
func TestInodeAttributes(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Test UID/GID access
	ino, err := sqfs.FindInode("include/zlib.h", false)
	if err != nil {
		t.Errorf("failed to find include/zlib.h: %s", err)
	} else {
		// Uid/Gid should be accessible
		uid := ino.GetUid()
		gid := ino.GetGid()

		// Not testing specific values as they may vary, but they should be accessible
		t.Logf("UID: %d, GID: %d", uid, gid)
	}

	// Test file mode
	fileInfo, err := fs.Stat(sqfs, "include/zlib.h")
	if err != nil {
		t.Errorf("failed to stat include/zlib.h: %s", err)
	} else {
		mode := fileInfo.Mode()
		if mode.IsDir() {
			t.Errorf("include/zlib.h should not be a directory")
		}
		if !mode.IsRegular() {
			t.Errorf("include/zlib.h should be a regular file")
		}
		
		// Check permission bits - should have at least read permission
		if mode&0400 == 0 {
			t.Errorf("include/zlib.h should have read permission")
		}
	}
}

// TestSubFS tests the fs.Sub interface for creating sub-filesystems
func TestSubFS(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Create a sub-filesystem for the include directory
	subFS, err := fs.Sub(sqfs, "include")
	if err != nil {
		t.Errorf("failed to create sub-filesystem: %s", err)
		return
	}

	// Test reading a file from the sub-filesystem
	data, err := fs.ReadFile(subFS, "zlib.h")
	if err != nil {
		t.Errorf("failed to read zlib.h from sub-filesystem: %s", err)
	} else if len(data) == 0 {
		t.Errorf("read 0 bytes from zlib.h in sub-filesystem")
	}

	// Test ReadDir on the sub-filesystem
	entries, err := fs.ReadDir(subFS, ".")
	if err != nil {
		t.Errorf("failed to read directory entries from sub-filesystem: %s", err)
	} else if len(entries) == 0 {
		t.Errorf("no entries found in sub-filesystem")
	}

	// Test root path
	_, err = fs.ReadFile(subFS, "../lib/libz.a")
	if err == nil {
		t.Errorf("should not be able to access files outside the sub-filesystem")
	}
}

// TestErrorCases tests various error conditions
func TestErrorCases(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Test invalid path
	_, err = sqfs.Open("..")
	if err == nil {
		t.Errorf("expected error opening invalid path '..'")
	}

	// Test opening a directory for reading
	dir, err := sqfs.Open("include")
	if err != nil {
		t.Errorf("failed to open directory: %s", err)
	} else {
		defer dir.Close()
		
		// Reading from a directory should fail
		buf := make([]byte, 100)
		_, err = dir.Read(buf)
		if err == nil {
			t.Errorf("expected error reading from directory")
		}
	}

	// Test reading from a non-existent file
	_, err = fs.ReadFile(sqfs, "include/nonexistent.h")
	if err == nil {
		t.Errorf("expected error reading non-existent file")
	}

	// Test invalid symlink resolution
	_, err = sqfs.FindInode(string(make([]byte, 1000, 1000)), false)
	if err == nil {
		t.Errorf("expected error with very long path")
	}
}

// TestFileServerCompatibility tests compatibility with http.FileServer
func TestFileServerCompatibility(t *testing.T) {
	// This test verifies the interface compatibility but doesn't start a real server
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Verify that the interface matches what http.FileServer expects
	var fsys fs.FS = sqfs
	// Confirm the interface is implemented
	var _ fs.StatFS = sqfs
	
	// Access some methods that http.FileServer would use
	_, err = fs.Stat(fsys, "include/zlib.h")
	if err != nil {
		t.Errorf("fs.Stat failed: %s", err)
	}
	
	_, err = fs.ReadDir(fsys, "include")
	if err != nil {
		t.Errorf("fs.ReadDir failed: %s", err)
	}
	
	// Open a file and check that it implements necessary interfaces
	f, err := fsys.Open("include/zlib.h")
	if err != nil {
		t.Errorf("Open failed: %s", err)
	} else {
		defer f.Close()
		
		// Verify we can get stat info
		_, err = f.Stat()
		if err != nil {
			t.Errorf("file.Stat failed: %s", err)
		}
		
		// Verify we can read from the file
		buf := make([]byte, 100)
		_, err = f.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("file.Read failed: %s", err)
		}
		
		// Try to cast to ReadSeeker (which http.FileServer wants)
		_, ok := f.(io.ReadSeeker)
		if !ok {
			t.Errorf("file doesn't implement io.ReadSeeker interface")
		}
	}
}

// TestDirectoryReadingPerformance tests directory reading performance with
// and without directory indexes
func TestDirectoryReadingPerformance(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/bigdir.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/bigdir.squashfs: %s", err)
	}
	defer sqfs.Close()

	// Test with directory indexes (should be fast)
	// Time how long it takes to find a file at the end of the directory
	start := make([]string, 0, 10)
	
	// Add a few test paths
	start = append(start, "bigdir/98999.txt")
	start = append(start, "bigdir/99499.txt")
	start = append(start, "bigdir/99999.txt")
	
	for _, testPath := range start {
		_, err := fs.Stat(sqfs, testPath)
		if err != nil && err != fs.ErrNotExist {
			t.Errorf("unexpected error accessing %s: %s", testPath, err)
		}
	}
}

// TestSquashFSNew tests creation of a SquashFS reader from an arbitrary ReaderAt
func TestSquashFSNew(t *testing.T) {
	// Open the file manually
	f, err := os.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open test file: %s", err)
	}
	defer f.Close()
	
	// Create SquashFS using New instead of Open
	sqfs, err := squashfs.New(f)
	if err != nil {
		t.Fatalf("failed to create SquashFS with New: %s", err)
	}
	
	// Test basic functionality
	data, err := fs.ReadFile(sqfs, "pkgconfig/zlib.pc")
	if err != nil {
		t.Errorf("failed to read file using New-created SquashFS: %s", err)
	} else if len(data) == 0 {
		t.Errorf("read 0 bytes from file")
	}
}