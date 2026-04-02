//go:build zstd

package squashfs_test

import (
	"testing"

	"github.com/KarpelesLab/squashfs"
)

func TestRoundTripCompressionZSTD(t *testing.T) {
	testCompressionRoundTrip(t, squashfs.ZSTD, nil)
}
