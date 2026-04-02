//go:build !unix

package squashfs

// getDevIno is a no-op on non-Unix platforms.
func getDevIno(sys any) (devIno, bool) {
	return devIno{}, false
}
