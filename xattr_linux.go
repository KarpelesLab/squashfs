//go:build linux

package squashfs

import (
	"io/fs"
	"syscall"
)

// readXattrs reads extended attributes from the source filesystem for the given path.
// If the FS implements XattrFS, that interface is used. Otherwise returns nil.
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

// listXattrSyscall lists xattr names on a file using syscall.
func listXattrSyscall(path string) ([]string, error) {
	size, err := syscall.Listxattr(path, nil)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	size, err = syscall.Listxattr(path, buf)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range splitNullTerminated(buf[:size]) {
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// getXattrSyscall gets a single xattr value using syscall.
func getXattrSyscall(path, name string) ([]byte, error) {
	size, err := syscall.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, size)
	size, err = syscall.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:size], nil
}

func splitNullTerminated(data []byte) []string {
	var result []string
	start := 0
	for i, b := range data {
		if b == 0 {
			result = append(result, string(data[start:i]))
			start = i + 1
		}
	}
	return result
}
