package squashfs_test

import (
	"io"
	"testing"

	"github.com/KarpelesLab/squashfs"
)

// mockReader implements io.ReaderAt and can be used to simulate
// errors or invalid data for testing error handling
type mockReader struct {
	data   []byte
	errAt  int64
	errMsg error
}

func (m *mockReader) ReadAt(p []byte, off int64) (int, error) {
	if m.errMsg != nil && off >= m.errAt {
		return 0, m.errMsg
	}
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestErrorHandling tests various error conditions using mock readers
func TestErrorHandling(t *testing.T) {
	// Test with invalid data (no magic header)
	invalidData := make([]byte, 100)
	mockInvalid := &mockReader{data: invalidData}

	_, err := squashfs.New(mockInvalid)
	if err == nil {
		t.Errorf("expected error with invalid data, got none")
	}

	// Test with truncated data
	// Create a mock header that has valid magic but is truncated
	truncatedData := []byte{'h', 's', 'q', 's'} // Valid magic in little endian
	for i := 0; i < 92; i++ {
		truncatedData = append(truncatedData, 0)
	}

	mockTruncated := &mockReader{
		data:   truncatedData,
		errAt:  20, // Set error after magic but before we can read full header
		errMsg: io.ErrUnexpectedEOF,
	}

	_, err = squashfs.New(mockTruncated)
	if err == nil {
		t.Errorf("expected error with truncated data, got none")
	}
}

// TestInvalidSuperblock tests handling of invalid superblock data
func TestInvalidSuperblock(t *testing.T) {
	// Create valid magic but invalid blocksize (mismatch between BlockSize and BlockLog)
	invalidBlockSizeData := []byte{'h', 's', 'q', 's'} // Valid magic in little endian

	// Fill with zeroes to match superblock size
	for i := 0; i < 92; i++ {
		invalidBlockSizeData = append(invalidBlockSizeData, 0)
	}

	// Set BlockSize to 4096 (bytes 12-16) but BlockLog to 11 (not 2^12) (bytes 22-24)
	// This creates an invalid combination
	copy(invalidBlockSizeData[12:16], []byte{0x00, 0x10, 0x00, 0x00}) // 4096 little-endian
	copy(invalidBlockSizeData[22:24], []byte{0x0B, 0x00})             // 11 little-endian

	mockInvalidBlockSize := &mockReader{data: invalidBlockSizeData}
	_, err := squashfs.New(mockInvalidBlockSize)
	if err == nil {
		t.Errorf("expected error with invalid block size, got none")
	}
}
