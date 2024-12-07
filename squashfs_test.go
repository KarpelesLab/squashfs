package squashfs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"testing"
	"time"

	"github.com/KarpelesLab/squashfs"
)

// testdata/zlib-dev.squashfs

func s256(buf []byte) string {
	hash := sha256.Sum256(buf)
	return hex.EncodeToString(hash[:])
}

func TestSquashfs(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/zlib-dev.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/zlib-dev.squashfs: %s", err)
	}
	defer sqfs.Close()

	data, err := fs.ReadFile(sqfs, "pkgconfig/zlib.pc")
	if err != nil {
		t.Errorf("failed to read pkgconfig/zlib.pc: %s", err)
	} else {
		//log.Printf("zlib.pc = %s", s256(data))
		if s256(data) != "2bbfca2364630d3ad2bbc9d44f45fe5470236539a906e11e2072157709e54692" {
			t.Errorf("invalid hash for pkgconfig/zlib.pc")
		}
	}

	// ensure we get the right inode
	ino, err := sqfs.FindInode("lib/libz.a", false)
	if err != nil {
		t.Errorf("failed to find lib/libz.a")
	} else {
		// should be inode 6
		if ino.Ino != 6 {
			t.Errorf("invalid inode found for lib/libz.a")
		}
	}

	// test glob (will test readdir etc)
	res, err := fs.Glob(sqfs, "lib/*.so")
	if err != nil {
		t.Errorf("failed to glob lib/*.so: %s", err)
	} else {
		if len(res) != 1 || res[0] != "lib/libz.so" {
			log.Printf("bad response for glob lib/*.so")
		}
	}

	st, err := fs.Stat(sqfs, "include/zlib.h")
	if err != nil {
		t.Errorf("failed to stat include/zlib.h: %s", err)
	} else {
		if st.Size() != 97323 {
			t.Errorf("bad file size on stat include/zlib.h")
		}
	}

	// test stat vs lstat
	st, err = fs.Stat(sqfs, "lib")
	if err != nil {
		t.Errorf("failed to stat lib: %s", err)
	} else if !st.IsDir() {
		t.Errorf("failed: stat(lib) did not return a directory")
	}

	st, err = sqfs.Lstat("lib")
	if err != nil {
		t.Errorf("failed to lstat lib: %s", err)
	} else if st.IsDir() {
		t.Errorf("failed: lstat(lib) should have returned something that is not a directory")
	}

	// test error
	_, err = fs.ReadFile(sqfs, "pkgconfig/zlib.pc/foo")
	if !errors.Is(err, squashfs.ErrNotDirectory) {
		t.Errorf("readfile pkgconfig/zlib.pc/foo returned unexpected err=%s", err)
	}

	// test other error
	_, err = sqfs.FindInode("lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/../lib/libz.a", false)
	if !errors.Is(err, squashfs.ErrTooManySymlinks) {
		t.Errorf("readfile lib/../lib/../(...)/libz.a returned unexpected err=%s", err)
	}
}

func TestBigdir(t *testing.T) {
	sqfs, err := squashfs.Open("testdata/bigdir.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/bigdir.squashfs: %s", err)
	}
	defer sqfs.Close()

	t1 := time.Now()
	data, err := fs.ReadFile(sqfs, "bigdir/99999.txt")
	d := time.Since(t1)
	if err != nil {
		t.Errorf("failed to read bigdir/99999.txt: %s", err)
	} else {
		//log.Printf("zlib.pc = %s", s256(data))
		if string(data) != "" {
			t.Errorf("invalid value for bigdir/99999.txt")
		}

		if d > 2*time.Millisecond {
			t.Errorf("read of bigdir/99999.txt took too long: %s (expected sub-millisecond read time)", d)
		}
	}

	data, err = fs.ReadFile(sqfs, "bigdir/999.txt")
	if err != nil {
		t.Errorf("failed to read bigdir/999.txt: %s", err)
	} else if string(data) != "" {
		t.Errorf("invalid value for bigdir/999.txt")
	}

	_, err = fs.ReadFile(sqfs, "bigdir/999999.txt")
	if err == nil {
		t.Errorf("failed to fail to read bigdir/999999.txt: %s", err)
	}
	_, err = fs.ReadFile(sqfs, "bigdir/12345.txt")
	if err != nil {
		t.Errorf("failed to read bigdir/12345.txt: %s", err)
	}
	_, err = fs.ReadFile(sqfs, "bigdir/76543.txt")
	if err != nil {
		t.Errorf("failed to read bigdir/76543.txt: %s", err)
	}

	// test for failure on:
	// ~/pkg/main/azusa.symlinks.core/full/lib64/libLLVMIRReader.a
	sqfs, err = squashfs.Open("testdata/azusa_symlinks.squashfs")
	if err != nil {
		t.Fatalf("failed to open testdata/azusa_symlinks.squashfs: %s", err)
	}
	defer sqfs.Close()

	_, err = sqfs.FindInode("full/lib64/libLLVMIRReader.a", false)
	if err != nil {
		t.Errorf("failed to find inode full/lib64/libLLVMIRReader.a: %s", err)
	}
}
