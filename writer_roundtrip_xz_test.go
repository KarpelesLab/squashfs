//go:build xz

package squashfs_test

import (
	"testing"

	"github.com/KarpelesLab/squashfs"
)

func TestRoundTripCompressionXZ(t *testing.T) {
	testCompressionRoundTrip(t, squashfs.XZ, nil)
}
