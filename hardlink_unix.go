//go:build unix

package squashfs

import "syscall"

// getDevIno extracts the device and inode numbers from a FileInfo.Sys() value.
func getDevIno(sys any) (devIno, bool) {
	if sys == nil {
		return devIno{}, false
	}
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return devIno{}, false
	}
	return devIno{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
