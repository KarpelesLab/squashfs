package squashfs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log"
	"testing"

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
		t.Errorf("failed to stat include/zlib.h")
	} else {
		if st.Size() != 97323 {
			t.Errorf("bad file size on stat include/zlib.h")
		}
	}
}
