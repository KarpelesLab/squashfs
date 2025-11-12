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

func TestWriterLongFilenames(t *testing.T) {
	// Test with very long filenames (up to 255 characters, which is common filesystem limit)
	testFS := make(fstest.MapFS)

	// Create filenames of various lengths
	lengths := []int{50, 100, 150, 200, 250, 255}
	for _, length := range lengths {
		name := strings.Repeat("a", length-4) + ".txt"
		testFS[name] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("content of %d char filename", length)),
		}
	}

	// Also add a long filename in a subdirectory
	longDirName := strings.Repeat("d", 100)
	longFileName := strings.Repeat("f", 150) + ".txt"
	testFS[longDirName+"/"+longFileName] = &fstest.MapFile{
		Data: []byte("nested long filename"),
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with long filenames: %d bytes", buf.Len())

	// Read it back and verify
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify all files can be read
	for _, length := range lengths {
		name := strings.Repeat("a", length-4) + ".txt"
		data, err := fs.ReadFile(sqfs, name)
		if err != nil {
			t.Fatalf("Failed to read file with %d char name: %s", length, err)
		}
		expected := fmt.Sprintf("content of %d char filename", length)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}

	// Verify nested long filename
	data, err := fs.ReadFile(sqfs, longDirName+"/"+longFileName)
	if err != nil {
		t.Fatalf("Failed to read nested long filename: %s", err)
	}
	if string(data) != "nested long filename" {
		t.Errorf("Expected 'nested long filename', got '%s'", string(data))
	}
}

func TestWriterVeryDeepNesting(t *testing.T) {
	// Test with very deep directory nesting (20+ levels)
	testFS := make(fstest.MapFS)

	// Create 25 levels of nesting
	depth := 25
	var pathComponents []string
	for i := 0; i < depth; i++ {
		pathComponents = append(pathComponents, fmt.Sprintf("level%02d", i))
	}
	deepPath := strings.Join(pathComponents, "/")

	// Add files at various depths
	for i := 5; i <= depth; i += 5 {
		path := strings.Join(pathComponents[:i], "/")
		testFS[path+"/file.txt"] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("file at depth %d", i)),
		}
	}

	// Add file at the deepest level
	testFS[deepPath+"/deepest.txt"] = &fstest.MapFile{
		Data: []byte("deepest file"),
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with %d levels of nesting: %d bytes", depth, buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify deepest file
	data, err := fs.ReadFile(sqfs, deepPath+"/deepest.txt")
	if err != nil {
		t.Fatalf("Failed to read deepest file: %s", err)
	}
	if string(data) != "deepest file" {
		t.Errorf("Expected 'deepest file', got '%s'", string(data))
	}

	// Verify files at various depths
	for i := 5; i <= depth; i += 5 {
		path := strings.Join(pathComponents[:i], "/")
		data, err := fs.ReadFile(sqfs, path+"/file.txt")
		if err != nil {
			t.Fatalf("Failed to read file at depth %d: %s", i, err)
		}
		expected := fmt.Sprintf("file at depth %d", i)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}
}

func TestWriterWideDirectoryTree(t *testing.T) {
	// Test with many directories at the same level (wide tree)
	testFS := make(fstest.MapFS)

	// Create 500 directories at the root level
	numDirs := 500
	for i := 0; i < numDirs; i++ {
		dirName := fmt.Sprintf("dir_%04d", i)
		testFS[dirName+"/file1.txt"] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("file in %s", dirName)),
		}
		testFS[dirName+"/file2.txt"] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("another file in %s", dirName)),
		}
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with %d directories: %d bytes", numDirs, buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify root directory has all subdirectories
	entries, err := sqfs.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read root directory: %s", err)
	}
	if len(entries) != numDirs {
		t.Errorf("Expected %d directories, got %d", numDirs, len(entries))
	}

	// Verify some random directories
	testDirs := []int{0, 50, 250, 499}
	for _, i := range testDirs {
		dirName := fmt.Sprintf("dir_%04d", i)
		data, err := fs.ReadFile(sqfs, dirName+"/file1.txt")
		if err != nil {
			t.Fatalf("Failed to read %s/file1.txt: %s", dirName, err)
		}
		expected := fmt.Sprintf("file in %s", dirName)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}
}

func TestWriterSpecialCharactersInNames(t *testing.T) {
	// Test with special characters in filenames
	testFS := make(fstest.MapFS)

	// Various special characters (that are valid in filenames)
	specialNames := []string{
		"file-with-dashes.txt",
		"file_with_underscores.txt",
		"file.with.dots.txt",
		"file with spaces.txt",
		"file(with)parens.txt",
		"file[with]brackets.txt",
		"file{with}braces.txt",
		"file@with@at.txt",
		"file#with#hash.txt",
		"file$with$dollar.txt",
		"file%with%percent.txt",
		"file&with&ampersand.txt",
		"file+with+plus.txt",
		"file=with=equals.txt",
		"file~with~tilde.txt",
		"file,with,commas.txt",
		"file;with;semicolons.txt",
		"file'with'quotes.txt",
	}

	for _, name := range specialNames {
		testFS[name] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("content of %s", name)),
		}
	}

	// Add some in subdirectories too
	testFS["special-dir/file!exclamation.txt"] = &fstest.MapFile{
		Data: []byte("file with exclamation"),
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with special characters: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify all files can be read
	for _, name := range specialNames {
		data, err := fs.ReadFile(sqfs, name)
		if err != nil {
			t.Fatalf("Failed to read '%s': %s", name, err)
		}
		expected := fmt.Sprintf("content of %s", name)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}

	// Verify nested special character file
	data, err := fs.ReadFile(sqfs, "special-dir/file!exclamation.txt")
	if err != nil {
		t.Fatalf("Failed to read special-dir/file!exclamation.txt: %s", err)
	}
	if string(data) != "file with exclamation" {
		t.Errorf("Expected 'file with exclamation', got '%s'", string(data))
	}
}

func TestWriterMixedWideAndDeep(t *testing.T) {
	// Test with both wide and deep directory structures
	testFS := make(fstest.MapFS)

	// Create a wide+deep tree: 10 levels deep, each with 10 subdirectories
	for level := 0; level < 5; level++ {
		for branch := 0; branch < 10; branch++ {
			// Build path
			var pathParts []string
			for l := 0; l <= level; l++ {
				if l < level {
					pathParts = append(pathParts, fmt.Sprintf("L%d_B%d", l, branch))
				} else {
					pathParts = append(pathParts, fmt.Sprintf("L%d_B%d", l, branch))
				}
			}
			path := strings.Join(pathParts, "/")

			// Add file at this location
			testFS[path+"/data.txt"] = &fstest.MapFile{
				Data: []byte(fmt.Sprintf("data at level %d branch %d", level, branch)),
			}
		}
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with wide+deep structure: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify some random paths
	testPaths := []struct {
		level  int
		branch int
	}{
		{0, 0}, {0, 9}, {2, 5}, {4, 3}, {4, 9},
	}

	for _, tp := range testPaths {
		var pathParts []string
		for l := 0; l <= tp.level; l++ {
			pathParts = append(pathParts, fmt.Sprintf("L%d_B%d", l, tp.branch))
		}
		path := strings.Join(pathParts, "/") + "/data.txt"

		data, err := fs.ReadFile(sqfs, path)
		if err != nil {
			t.Fatalf("Failed to read %s: %s", path, err)
		}
		expected := fmt.Sprintf("data at level %d branch %d", tp.level, tp.branch)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}
}

func TestWriterEmptyDirectoriesAtVariousLevels(t *testing.T) {
	// Test with empty directories at various nesting levels
	testFS := make(fstest.MapFS)

	// Create empty directories by creating a deep structure
	// then only adding files to some branches
	for i := 0; i < 5; i++ {
		// Create a branch with a file (non-empty)
		path := fmt.Sprintf("branch%d/level1/level2", i)
		testFS[path+"/file.txt"] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("file in branch %d", i)),
		}

		// Create empty intermediate directories by adding a deep file elsewhere
		emptyPath := fmt.Sprintf("branch%d/empty/deep/path", i)
		testFS[emptyPath+"/.keep"] = &fstest.MapFile{
			Data: []byte(""),
		}
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with empty directories: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify we can navigate through empty directories
	for i := 0; i < 5; i++ {
		// Check that intermediate empty directories exist
		emptyDirs := []string{
			fmt.Sprintf("branch%d/empty", i),
			fmt.Sprintf("branch%d/empty/deep", i),
		}

		for _, dir := range emptyDirs {
			entries, err := sqfs.ReadDir(dir)
			if err != nil {
				t.Fatalf("Failed to read directory %s: %s", dir, err)
			}
			// Directory should exist (even if empty or only containing subdirs)
			t.Logf("Directory %s has %d entries", dir, len(entries))
		}
	}
}

func TestWriterLargeNumberOfInodes(t *testing.T) {
	// Test with a large number of inodes (directories + files)
	testFS := make(fstest.MapFS)

	// Create 100 directories, each with 50 files
	numDirs := 100
	filesPerDir := 50

	for d := 0; d < numDirs; d++ {
		dirName := fmt.Sprintf("dir%03d", d)
		for f := 0; f < filesPerDir; f++ {
			fileName := fmt.Sprintf("file%03d.dat", f)
			testFS[dirName+"/"+fileName] = &fstest.MapFile{
				Data: []byte(fmt.Sprintf("data-%d-%d", d, f)),
			}
		}
	}

	totalFiles := numDirs * filesPerDir
	t.Logf("Creating SquashFS with %d files in %d directories", totalFiles, numDirs)

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with %d inodes: %d bytes", totalFiles+numDirs+1, buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify root has correct number of directories
	entries, err := sqfs.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read root directory: %s", err)
	}
	if len(entries) != numDirs {
		t.Errorf("Expected %d directories in root, got %d", numDirs, len(entries))
	}

	// Spot check some files
	testFiles := []struct {
		dir  int
		file int
	}{
		{0, 0}, {0, 49}, {50, 25}, {99, 0}, {99, 49},
	}

	for _, tf := range testFiles {
		path := fmt.Sprintf("dir%03d/file%03d.dat", tf.dir, tf.file)
		data, err := fs.ReadFile(sqfs, path)
		if err != nil {
			t.Fatalf("Failed to read %s: %s", path, err)
		}
		expected := fmt.Sprintf("data-%d-%d", tf.dir, tf.file)
		if string(data) != expected {
			t.Errorf("Expected '%s', got '%s'", expected, string(data))
		}
	}
}

func TestWriterVeryLongPath(t *testing.T) {
	// Test with a very long total path (combining directory depth and filename length)
	testFS := make(fstest.MapFS)

	// Create a path with total length around 1000 characters
	// Use 10 directories of 90 chars each, plus a 100 char filename
	var pathParts []string
	for i := 0; i < 10; i++ {
		dirName := strings.Repeat(fmt.Sprintf("d%d", i), 44) // ~88 chars
		pathParts = append(pathParts, dirName)
	}
	fileName := strings.Repeat("f", 96) + ".txt" // 100 chars

	fullPath := strings.Join(pathParts, "/") + "/" + fileName
	t.Logf("Testing with path length: %d characters", len(fullPath))

	testFS[fullPath] = &fstest.MapFile{
		Data: []byte("content in very long path"),
	}

	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)
	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	t.Logf("Created SquashFS with very long path: %d bytes", buf.Len())

	// Read it back
	sqfs, err := squashfs.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read back SquashFS: %s", err)
	}

	// Verify we can read the file
	data, err := fs.ReadFile(sqfs, fullPath)
	if err != nil {
		t.Fatalf("Failed to read file with very long path: %s", err)
	}
	if string(data) != "content in very long path" {
		t.Errorf("Expected 'content in very long path', got '%s'", string(data))
	}
}
