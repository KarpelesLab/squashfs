//go:build !linux

package squashfs

import "io/fs"

// readXattrs is a no-op on non-Linux platforms unless the FS implements XattrFS.
func readXattrs(srcFS fs.FS, path string) map[string][]byte {
	if xfs, ok := srcFS.(XattrFS); ok {
		names, err := xfs.ListXattr(path)
		if err != nil || len(names) == 0 {
			return nil
		}
		result := make(map[string][]byte)
		for _, name := range names {
			val, err := xfs.GetXattr(path, name)
			if err != nil {
				continue
			}
			result[name] = val
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}
	return nil
}
