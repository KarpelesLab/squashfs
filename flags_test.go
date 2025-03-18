package squashfs_test

import (
	"testing"

	"github.com/KarpelesLab/squashfs"
)

// TestFlagsOperations tests the Flags type operations
func TestFlagsOperations(t *testing.T) {
	// Test flag string representation
	testCases := []struct {
		flag     squashfs.Flags
		expected string
	}{
		{squashfs.UNCOMPRESSED_INODES, "UNCOMPRESSED_INODES"},
		{squashfs.UNCOMPRESSED_DATA, "UNCOMPRESSED_DATA"},
		{squashfs.CHECK, "CHECK"},
		{squashfs.UNCOMPRESSED_FRAGMENTS, "UNCOMPRESSED_FRAGMENTS"},
		{squashfs.NO_FRAGMENTS, "NO_FRAGMENTS"},
		{squashfs.ALWAYS_FRAGMENTS, "ALWAYS_FRAGMENTS"},
		{squashfs.DUPLICATES, "DUPLICATES"},
		{squashfs.EXPORTABLE, "EXPORTABLE"},
		{squashfs.UNCOMPRESSED_XATTRS, "UNCOMPRESSED_XATTRS"},
		{squashfs.NO_XATTRS, "NO_XATTRS"},
		{squashfs.COMPRESSOR_OPTIONS, "COMPRESSOR_OPTIONS"},
		{squashfs.UNCOMPRESSED_IDS, "UNCOMPRESSED_IDS"},
		{squashfs.EXPORTABLE | squashfs.NO_FRAGMENTS, "NO_FRAGMENTS|EXPORTABLE"},
		{0, ""},
		{1<<15 | 1<<14, ""}, // Unknown flags
	}

	for _, tc := range testCases {
		if tc.flag.String() != tc.expected {
			t.Errorf("Expected flag %d string to be %s, got %s", tc.flag, tc.expected, tc.flag.String())
		}
	}

	// Test Has method
	flags := squashfs.EXPORTABLE | squashfs.UNCOMPRESSED_DATA
	
	if !flags.Has(squashfs.EXPORTABLE) {
		t.Errorf("flags should have EXPORTABLE")
	}
	
	if !flags.Has(squashfs.UNCOMPRESSED_DATA) {
		t.Errorf("flags should have UNCOMPRESSED_DATA")
	}
	
	if flags.Has(squashfs.NO_FRAGMENTS) {
		t.Errorf("flags should not have NO_FRAGMENTS")
	}
}