package squashfs

import (
	"bytes"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestDebugWriterEntries(t *testing.T) {
	testFS := make(fstest.MapFS)

	// Create 100 directories with 50 files each (matching the failing test)
	for d := 0; d < 100; d++ {
		for f := 0; f < 50; f++ {
			path := fmt.Sprintf("dir%03d/file%03d.txt", d, f)
			testFS[path] = &fstest.MapFile{
				Data: []byte(fmt.Sprintf("data %d %d", d, f)),
			}
		}
	}

	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}

	w.SetSourceFS(testFS)

	err = fs.WalkDir(testFS, ".", w.Add)
	if err != nil {
		t.Fatalf("WalkDir failed: %s", err)
	}

	t.Logf("Total inodes: %d", w.inodeCount)
	t.Logf("Root has %d entries", len(w.rootInode.entries))

	err = w.Finalize()
	if err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}

	data := buf.Bytes()
	t.Logf("Total size: %d bytes", len(data))

	// Read it back
	sqfs, err := New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Failed to read back: %s", err)
	}

	entries, err := sqfs.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read root: %s", err)
	}
	t.Logf("Read back %d entries from root", len(entries))

	if len(entries) != 100 {
		t.Errorf("Expected 100 entries, got %d", len(entries))
		// List what we got
		for i, e := range entries {
			if i < 5 || i >= len(entries)-5 {
				t.Logf("  Entry %d: %s", i, e.Name())
			}
		}
	}

	// Try to access each directory
	for d := 0; d < 100; d++ {
		name := fmt.Sprintf("dir%03d", d)
		subEntries, err := sqfs.ReadDir(name)
		if err != nil {
			t.Errorf("ReadDir %s: %v", name, err)
		} else if len(subEntries) != 50 {
			t.Errorf("%s: expected 50 entries, got %d", name, len(subEntries))
		}
	}
}
