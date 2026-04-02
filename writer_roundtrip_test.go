package squashfs_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/KarpelesLab/squashfs"
)

// helper to create a squashfs image, finalize it, and return the bytes.
func buildImage(t *testing.T, opts []squashfs.WriterOption, populate func(w *squashfs.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := squashfs.NewWriter(&buf, opts...)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	populate(w)
	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %s", err)
	}
	return buf.Bytes()
}

// helper to open a squashfs image from bytes.
func openImage(t *testing.T, data []byte) *squashfs.Superblock {
	t.Helper()
	sb, err := squashfs.New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Failed to read squashfs: %s", err)
	}
	return sb
}

func TestRoundTripPermissions(t *testing.T) {
	perms := []struct {
		name string
		mode fs.FileMode
	}{
		{"0644", 0644},
		{"0755", 0755},
		{"0600", 0600},
		{"0777", 0777},
		{"setuid_04755", fs.ModeSetuid | 0755},
		{"setgid_02755", fs.ModeSetgid | 0755},
		{"sticky_01777", fs.ModeSticky | 0777},
		{"all_07777", fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky | 0777},
	}

	data := buildImage(t, nil, func(w *squashfs.Writer) {
		for _, p := range perms {
			if err := w.AddFile(p.name+".txt", []byte("test"), p.mode); err != nil {
				t.Fatalf("AddFile %s: %s", p.name, err)
			}
		}
	})

	sb := openImage(t, data)

	for _, p := range perms {
		t.Run(p.name, func(t *testing.T) {
			info, err := sb.Stat(p.name + ".txt")
			if err != nil {
				t.Fatalf("Stat failed: %s", err)
			}
			got := info.Mode()
			// Compare permission + special bits (strip file type bits)
			gotPerm := got & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
			wantPerm := p.mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
			if gotPerm != wantPerm {
				t.Errorf("permissions mismatch: got %04o, want %04o", gotPerm, wantPerm)
			}
		})
	}

	// Cross-validate with unsquashfs
	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
	}
}

func TestRoundTripOwnership(t *testing.T) {
	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("root_file.txt", []byte("owned by root"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("root_file.txt", 0, 0); err != nil {
			t.Fatal(err)
		}

		if err := w.AddFile("user_file.txt", []byte("owned by user"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("user_file.txt", 1000, 1000); err != nil {
			t.Fatal(err)
		}

		if err := w.AddFile("mixed_file.txt", []byte("mixed ownership"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("mixed_file.txt", 65534, 100); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	tests := []struct {
		path    string
		wantUID uint32
		wantGID uint32
	}{
		{"root_file.txt", 0, 0},
		{"user_file.txt", 1000, 1000},
		{"mixed_file.txt", 65534, 100},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			info, err := sb.Stat(tt.path)
			if err != nil {
				t.Fatalf("Stat failed: %s", err)
			}
			ino := info.Sys().(*squashfs.Inode)
			gotUID := ino.GetUid()
			gotGID := ino.GetGid()
			if gotUID != tt.wantUID {
				t.Errorf("uid mismatch: got %d, want %d", gotUID, tt.wantUID)
			}
			if gotGID != tt.wantGID {
				t.Errorf("gid mismatch: got %d, want %d", gotGID, tt.wantGID)
			}
		})
	}

	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
	}
}

func TestRoundTripTimestamps(t *testing.T) {
	ts1 := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts3 := time.Date(2000, 3, 14, 9, 26, 53, 0, time.UTC)

	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("file1.txt", []byte("content1"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetModTime("file1.txt", ts1); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("file2.txt", []byte("content2"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetModTime("file2.txt", ts2); err != nil {
			t.Fatal(err)
		}
		if err := w.AddDirectory("dir", 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.SetModTime("dir", ts3); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	tests := []struct {
		path string
		want time.Time
	}{
		{"file1.txt", ts1},
		{"file2.txt", ts2},
		{"dir", ts3},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			info, err := sb.Stat(tt.path)
			if err != nil {
				t.Fatalf("Stat failed: %s", err)
			}
			got := info.ModTime()
			if !got.Equal(tt.want) {
				t.Errorf("ModTime mismatch: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRoundTripSymlinks(t *testing.T) {
	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("target.txt", []byte("target content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("relative_link", "target.txt"); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("absolute_link", "/usr/bin/something"); err != nil {
			t.Fatal(err)
		}
		if err := w.AddDirectory("subdir", 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("subdir/back_link", "../target.txt"); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	tests := []struct {
		path   string
		target string
	}{
		{"relative_link", "target.txt"},
		{"absolute_link", "/usr/bin/something"},
		{"subdir/back_link", "../target.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			info, err := sb.Lstat(tt.path)
			if err != nil {
				t.Fatalf("Lstat failed: %s", err)
			}
			if info.Mode()&fs.ModeSymlink == 0 {
				t.Error("expected symlink mode")
			}

			ino := info.Sys().(*squashfs.Inode)
			got, err := ino.Readlink()
			if err != nil {
				t.Fatalf("Readlink failed: %s", err)
			}
			if string(got) != tt.target {
				t.Errorf("symlink target: got %q, want %q", string(got), tt.target)
			}
		})
	}

	// Verify file content through symlink
	content, err := fs.ReadFile(sb, "relative_link")
	if err != nil {
		t.Fatalf("ReadFile through symlink: %s", err)
	}
	if string(content) != "target content" {
		t.Errorf("content through symlink: got %q, want %q", string(content), "target content")
	}

	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
		// Verify symlink targets appear in output
		if !strings.Contains(out, "target.txt") {
			t.Error("unsquashfs output missing symlink target 'target.txt'")
		}
	}
}

func TestRoundTripDevices(t *testing.T) {
	// major=1, minor=3 → /dev/null (char device)
	// major=8, minor=0 → /dev/sda (block device)
	nullRdev := uint32(1<<8 | 3) // makedev(1,3)
	sdaRdev := uint32(8 << 8)    // makedev(8,0)

	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddDevice("char_dev", fs.ModeCharDevice|0666, nullRdev); err != nil {
			t.Fatal(err)
		}
		if err := w.AddDevice("block_dev", fs.ModeDevice|0660, sdaRdev); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	t.Run("char_dev", func(t *testing.T) {
		info, err := sb.Stat("char_dev")
		if err != nil {
			t.Fatalf("Stat failed: %s", err)
		}
		if info.Mode()&fs.ModeCharDevice == 0 {
			t.Errorf("expected char device mode, got %v", info.Mode())
		}
		ino := info.Sys().(*squashfs.Inode)
		if ino.Rdev != nullRdev {
			t.Errorf("rdev mismatch: got %d, want %d", ino.Rdev, nullRdev)
		}
	})

	t.Run("block_dev", func(t *testing.T) {
		info, err := sb.Stat("block_dev")
		if err != nil {
			t.Fatalf("Stat failed: %s", err)
		}
		if info.Mode()&fs.ModeDevice == 0 {
			t.Errorf("expected block device mode, got %v", info.Mode())
		}
		ino := info.Sys().(*squashfs.Inode)
		if ino.Rdev != sdaRdev {
			t.Errorf("rdev mismatch: got %d, want %d", ino.Rdev, sdaRdev)
		}
	})

	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
	}
}

func TestRoundTripFifosAndSockets(t *testing.T) {
	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFifo("my_fifo", 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSocket("my_socket", 0755); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	t.Run("fifo", func(t *testing.T) {
		info, err := sb.Stat("my_fifo")
		if err != nil {
			t.Fatalf("Stat failed: %s", err)
		}
		if info.Mode()&fs.ModeNamedPipe == 0 {
			t.Errorf("expected fifo mode, got %v", info.Mode())
		}
		gotPerm := info.Mode().Perm()
		if gotPerm != 0644 {
			t.Errorf("perm mismatch: got %04o, want 0644", gotPerm)
		}
	})

	t.Run("socket", func(t *testing.T) {
		info, err := sb.Stat("my_socket")
		if err != nil {
			t.Fatalf("Stat failed: %s", err)
		}
		if info.Mode()&fs.ModeSocket == 0 {
			t.Errorf("expected socket mode, got %v", info.Mode())
		}
		gotPerm := info.Mode().Perm()
		if gotPerm != 0755 {
			t.Errorf("perm mismatch: got %04o, want 0755", gotPerm)
		}
	})

	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
	}
}

func TestRoundTripMixed(t *testing.T) {
	ts := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	data := buildImage(t, nil, func(w *squashfs.Writer) {
		// Regular files
		if err := w.AddFile("readme.txt", []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("readme.txt", 1000, 1000); err != nil {
			t.Fatal(err)
		}
		if err := w.SetModTime("readme.txt", ts); err != nil {
			t.Fatal(err)
		}

		// Executable
		if err := w.AddFile("bin/app", []byte("#!/bin/sh\necho hi"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("bin/app", 0, 0); err != nil {
			t.Fatal(err)
		}

		// Setuid binary
		if err := w.AddFile("bin/suid", []byte("binary"), fs.ModeSetuid|0755); err != nil {
			t.Fatal(err)
		}

		// Symlinks
		if err := w.AddSymlink("link_to_readme", "readme.txt"); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("bin/lib_link", "/usr/lib"); err != nil {
			t.Fatal(err)
		}

		// Devices
		if err := w.AddDevice("dev/null", fs.ModeCharDevice|0666, uint32(1<<8|3)); err != nil {
			t.Fatal(err)
		}
		if err := w.AddDevice("dev/sda", fs.ModeDevice|0660, uint32(8<<8)); err != nil {
			t.Fatal(err)
		}

		// Fifo and socket
		if err := w.AddFifo("run/pipe", 0600); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSocket("run/sock", 0700); err != nil {
			t.Fatal(err)
		}

		// Empty directory
		if err := w.AddDirectory("empty", 0755); err != nil {
			t.Fatal(err)
		}

		// Large file (multi-block)
		bigData := bytes.Repeat([]byte("ABCDEFGHIJ"), 20000) // 200KB
		if err := w.AddFile("data/large.bin", bigData, 0444); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	// Verify regular file
	content, err := fs.ReadFile(sb, "readme.txt")
	if err != nil {
		t.Fatalf("ReadFile: %s", err)
	}
	if string(content) != "hello world" {
		t.Errorf("content mismatch: got %q", string(content))
	}

	info, err := sb.Stat("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	ino := info.Sys().(*squashfs.Inode)
	if ino.GetUid() != 1000 || ino.GetGid() != 1000 {
		t.Errorf("uid/gid mismatch: got %d/%d", ino.GetUid(), ino.GetGid())
	}
	if !info.ModTime().Equal(ts) {
		t.Errorf("modtime mismatch: got %v, want %v", info.ModTime(), ts)
	}

	// Verify setuid
	info, err = sb.Stat("bin/suid")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSetuid == 0 {
		t.Error("expected setuid bit")
	}

	// Verify symlink
	info, err = sb.Lstat("link_to_readme")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Error("expected symlink")
	}

	// Verify device
	info, err = sb.Stat("dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeCharDevice == 0 {
		t.Error("expected char device")
	}

	// Verify fifo
	info, err = sb.Stat("run/pipe")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeNamedPipe == 0 {
		t.Error("expected fifo")
	}

	// Verify large file
	largeContent, err := fs.ReadFile(sb, "data/large.bin")
	if err != nil {
		t.Fatal(err)
	}
	expectedLarge := bytes.Repeat([]byte("ABCDEFGHIJ"), 20000)
	if !bytes.Equal(largeContent, expectedLarge) {
		t.Errorf("large file mismatch: got %d bytes, want %d", len(largeContent), len(expectedLarge))
	}

	// Verify directory listing
	entries, err := sb.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Root has %d entries", len(entries))

	// Cross-validate
	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)
	}
}

func TestRoundTripCompressionGZip(t *testing.T) {
	testCompressionRoundTrip(t, squashfs.GZip, nil)
}

// testCompressionRoundTrip is a reusable compression round-trip test.
func testCompressionRoundTrip(t *testing.T, comp squashfs.Compression, opts []squashfs.WriterOption) {
	t.Helper()

	allOpts := append([]squashfs.WriterOption{squashfs.WithCompression(comp)}, opts...)

	data := buildImage(t, allOpts, func(w *squashfs.Writer) {
		// Small file (may store uncompressed if compression doesn't help)
		if err := w.AddFile("small.txt", []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}

		// Medium file (compressible)
		medium := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog.\n"), 500)
		if err := w.AddFile("medium.txt", medium, 0644); err != nil {
			t.Fatal(err)
		}

		// Large file (multi-block, tests block-level compression)
		large := bytes.Repeat([]byte("COMPRESS_ME_"), 20000) // 240KB
		if err := w.AddFile("large.bin", large, 0644); err != nil {
			t.Fatal(err)
		}

		// Random-ish data (less compressible)
		random := make([]byte, 10000)
		for i := range random {
			random[i] = byte(i*37 + i*i*13)
		}
		if err := w.AddFile("random.bin", random, 0644); err != nil {
			t.Fatal(err)
		}

		// Directory structure
		if err := w.AddDirectory("dir", 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("dir/nested.txt", []byte("nested content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("dir/link", "../small.txt"); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	// Verify compression type
	if sb.Comp != comp {
		t.Errorf("compression mismatch: got %s, want %s", sb.Comp, comp)
	}

	// Verify all files
	content, err := fs.ReadFile(sb, "small.txt")
	if err != nil {
		t.Fatalf("ReadFile small.txt: %s", err)
	}
	if string(content) != "hello" {
		t.Errorf("small.txt content mismatch")
	}

	content, err = fs.ReadFile(sb, "medium.txt")
	if err != nil {
		t.Fatalf("ReadFile medium.txt: %s", err)
	}
	expected := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog.\n"), 500)
	if !bytes.Equal(content, expected) {
		t.Errorf("medium.txt content mismatch: got %d bytes, want %d", len(content), len(expected))
	}

	content, err = fs.ReadFile(sb, "large.bin")
	if err != nil {
		t.Fatalf("ReadFile large.bin: %s", err)
	}
	expectedLarge := bytes.Repeat([]byte("COMPRESS_ME_"), 20000)
	if !bytes.Equal(content, expectedLarge) {
		t.Errorf("large.bin content mismatch: got %d bytes, want %d", len(content), len(expectedLarge))
	}

	content, err = fs.ReadFile(sb, "random.bin")
	if err != nil {
		t.Fatalf("ReadFile random.bin: %s", err)
	}
	randomExpected := make([]byte, 10000)
	for i := range randomExpected {
		randomExpected[i] = byte(i*37 + i*i*13)
	}
	if !bytes.Equal(content, randomExpected) {
		t.Errorf("random.bin content mismatch")
	}

	t.Logf("Created %s image: %d bytes", comp, len(data))

	// Cross-validate with unsquashfs
	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln output:\n%s", out)

		// Also verify extraction works
		destDir := unsquashfsExtract(t, data)
		t.Logf("Extracted to %s", destDir)
	}
}

func TestUnsquashfsValidation(t *testing.T) {
	if !unsquashfsAvailable() {
		t.Skip("unsquashfs not available")
	}

	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("file.txt", []byte("test content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetOwner("file.txt", 1000, 1000); err != nil {
			t.Fatal(err)
		}

		if err := w.AddDirectory("dir", 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("dir/nested.txt", []byte("nested"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := w.AddSymlink("link", "file.txt"); err != nil {
			t.Fatal(err)
		}

		if err := w.AddDevice("null", fs.ModeCharDevice|0666, uint32(1<<8|3)); err != nil {
			t.Fatal(err)
		}

		if err := w.AddFifo("pipe", 0644); err != nil {
			t.Fatal(err)
		}
	})

	// Basic listing should work
	out := unsquashfsListLong(t, data)
	t.Logf("unsquashfs -lln:\n%s", out)

	// Verify key entries appear in output
	checks := []string{
		"file.txt",
		"dir",
		"nested.txt",
		"link",
		"null",
		"pipe",
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("unsquashfs output missing %q", check)
		}
	}

	// Verify symlink target in output
	if !strings.Contains(out, "file.txt") {
		t.Error("unsquashfs output missing symlink target")
	}

	// Full stat output
	statOut := runUnsquashfs(t, data, "-stat")
	t.Logf("unsquashfs -stat:\n%s", statOut)
}

func TestRoundTripFragments(t *testing.T) {
	// Many small files — all should go into fragments
	data := buildImage(t, nil, func(w *squashfs.Writer) {
		for i := 0; i < 50; i++ {
			name := fmt.Sprintf("file_%03d.txt", i)
			content := fmt.Sprintf("content %d", i)
			if err := w.AddFile(name, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	})

	sb := openImage(t, data)

	if sb.FragCount == 0 {
		t.Error("expected FragCount > 0 for small files")
	}
	t.Logf("FragCount=%d, image size=%d bytes", sb.FragCount, len(data))

	// Verify all files round-trip
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("file_%03d.txt", i)
		content, err := fs.ReadFile(sb, name)
		if err != nil {
			t.Fatalf("ReadFile %s: %s", name, err)
		}
		expected := fmt.Sprintf("content %d", i)
		if string(content) != expected {
			t.Errorf("%s: got %q, want %q", name, string(content), expected)
		}
	}

	if unsquashfsAvailable() {
		out := unsquashfsListLong(t, data)
		if !strings.Contains(out, "file_000.txt") {
			t.Error("unsquashfs missing file_000.txt")
		}
		// Verify stat shows fragments
		statOut := runUnsquashfs(t, data, "-stat")
		t.Logf("unsquashfs -stat:\n%s", statOut)
		if !strings.Contains(statOut, "Number of fragments") {
			t.Error("missing fragment info in stat output")
		}
	}
}

func TestRoundTripFragmentMixedSizes(t *testing.T) {
	blockSize := uint32(4096) // small block size for testing
	data := buildImage(t, []squashfs.WriterOption{squashfs.WithBlockSize(blockSize)}, func(w *squashfs.Writer) {
		for _, tc := range []struct {
			name string
			data []byte
		}{
			{"tiny.txt", []byte("x")},
			{"almost_block.txt", bytes.Repeat([]byte("a"), int(blockSize)-1)},
			{"exact_block.txt", bytes.Repeat([]byte("b"), int(blockSize))},
			{"block_plus_one.txt", bytes.Repeat([]byte("c"), int(blockSize)+1)},
			{"two_blocks_plus.txt", bytes.Repeat([]byte("d"), int(blockSize)*2+50)},
		} {
			if err := w.AddFile(tc.name, tc.data, 0644); err != nil {
				t.Fatal(err)
			}
		}
	})

	sb := openImage(t, data)
	t.Logf("FragCount=%d, image size=%d", sb.FragCount, len(data))

	// Verify all content
	tests := []struct {
		name string
		size int
		fill byte
	}{
		{"tiny.txt", 1, 'x'},
		{"almost_block.txt", int(blockSize) - 1, 'a'},
		{"exact_block.txt", int(blockSize), 'b'},
		{"block_plus_one.txt", int(blockSize) + 1, 'c'},
		{"two_blocks_plus.txt", int(blockSize)*2 + 50, 'd'},
	}

	for _, tt := range tests {
		content, err := fs.ReadFile(sb, tt.name)
		if err != nil {
			t.Fatalf("ReadFile %s: %s", tt.name, err)
		}
		if len(content) != tt.size {
			t.Errorf("%s: got %d bytes, want %d", tt.name, len(content), tt.size)
		}
		for i, b := range content {
			if b != tt.fill {
				t.Errorf("%s: byte %d = %c, want %c", tt.name, i, b, tt.fill)
				break
			}
		}
	}

	if unsquashfsAvailable() {
		unsquashfsExtract(t, data)
	}
}

func TestCloneFile(t *testing.T) {
	content := bytes.Repeat([]byte("dedup test data\n"), 1000)

	// Image with clone
	cloneData := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("original.txt", content, 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.CloneInode("clone.txt", "original.txt"); err != nil {
			t.Fatal(err)
		}
	})

	// Image without clone (two copies)
	noCloneData := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("original.txt", content, 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("clone.txt", content, 0644); err != nil {
			t.Fatal(err)
		}
	})

	t.Logf("With clone: %d bytes, without: %d bytes, saving: %d bytes",
		len(cloneData), len(noCloneData), len(noCloneData)-len(cloneData))

	if len(cloneData) >= len(noCloneData) {
		t.Error("clone should produce smaller image")
	}

	// Verify both files readable
	sb := openImage(t, cloneData)
	for _, name := range []string{"original.txt", "clone.txt"} {
		got, err := fs.ReadFile(sb, name)
		if err != nil {
			t.Fatalf("ReadFile %s: %s", name, err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("%s content mismatch", name)
		}
	}

	if unsquashfsAvailable() {
		unsquashfsExtract(t, cloneData)
	}
}

func TestXattrRoundTrip(t *testing.T) {
	data := buildImage(t, nil, func(w *squashfs.Writer) {
		if err := w.AddFile("file.txt", []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := w.SetXattr("file.txt", "user.myattr", []byte("myvalue")); err != nil {
			t.Fatal(err)
		}
		if err := w.SetXattr("file.txt", "user.another", []byte("val2")); err != nil {
			t.Fatal(err)
		}
		if err := w.AddDirectory("dir", 0755); err != nil {
			t.Fatal(err)
		}
		if err := w.SetXattr("dir", "user.dirattr", []byte("dirval")); err != nil {
			t.Fatal(err)
		}
	})

	sb := openImage(t, data)

	// Verify files still readable
	content, err := fs.ReadFile(sb, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Errorf("content: got %q, want %q", string(content), "hello")
	}

	t.Logf("Image size: %d bytes, FragCount: %d", len(data), sb.FragCount)

	if unsquashfsAvailable() {
		// unsquashfs -lln shows xattrs
		out := unsquashfsListLong(t, data)
		t.Logf("unsquashfs -lln:\n%s", out)

		statOut := runUnsquashfs(t, data, "-stat")
		t.Logf("unsquashfs -stat:\n%s", statOut)
		if strings.Contains(statOut, "Number of xattr ids 0") {
			t.Error("expected xattr ids > 0")
		}
	}
}
