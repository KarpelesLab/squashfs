package squashfs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// unsquashfsAvailable returns true if unsquashfs is on PATH.
func unsquashfsAvailable() bool {
	_, err := exec.LookPath("unsquashfs")
	return err == nil
}

// writeToTempFile writes data to a temporary file and returns its path.
// The file is automatically cleaned up when the test ends.
func writeToTempFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "squashfs-test-*.sqfs")
	if err != nil {
		t.Fatalf("failed to create temp file: %s", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("failed to write temp file: %s", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("failed to close temp file: %s", err)
	}
	return f.Name()
}

// runUnsquashfs runs unsquashfs with the given squashfs data and arguments.
// It skips the test if unsquashfs is not available.
// Returns the combined stdout+stderr output.
func runUnsquashfs(t *testing.T, sqfsData []byte, args ...string) string {
	t.Helper()
	if !unsquashfsAvailable() {
		t.Skip("unsquashfs not available")
	}
	tmpFile := writeToTempFile(t, sqfsData)
	fullArgs := append(args, tmpFile)
	cmd := exec.Command("unsquashfs", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unsquashfs %v failed: %s\noutput: %s", args, err, string(out))
	}
	return string(out)
}

// unsquashfsListLong runs unsquashfs -lln on the data and returns the output.
func unsquashfsListLong(t *testing.T, sqfsData []byte) string {
	t.Helper()
	return runUnsquashfs(t, sqfsData, "-lln")
}

// unsquashfsExtract extracts the squashfs image to a temp directory and returns the path.
func unsquashfsExtract(t *testing.T, sqfsData []byte) string {
	t.Helper()
	if !unsquashfsAvailable() {
		t.Skip("unsquashfs not available")
	}
	tmpFile := writeToTempFile(t, sqfsData)
	destDir := filepath.Join(t.TempDir(), "extracted")
	cmd := exec.Command("unsquashfs", "-d", destDir, "-no-xattrs", tmpFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unsquashfs extract failed: %s\noutput: %s", err, string(out))
	}
	return destDir
}
