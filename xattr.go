package squashfs

import (
	"fmt"
	"io/fs"
	"strings"
)

// Xattr namespace type IDs as stored in squashfs.
const (
	XattrUser     uint16 = 0
	XattrTrusted  uint16 = 1
	XattrSecurity uint16 = 2
	XattrSystem   uint16 = 3
)

var xattrPrefixes = map[uint16]string{
	XattrUser:     "user.",
	XattrTrusted:  "trusted.",
	XattrSecurity: "security.",
	XattrSystem:   "system.",
}

// xattrParseKey splits a full xattr key like "user.foo" into (type=0, name="foo").
func xattrParseKey(key string) (uint16, string, error) {
	for typ, prefix := range xattrPrefixes {
		if strings.HasPrefix(key, prefix) {
			return typ, key[len(prefix):], nil
		}
	}
	return 0, "", fmt.Errorf("unknown xattr namespace: %s", key)
}

// XattrBuildKey reconstructs a full key from type and name.
func XattrBuildKey(typ uint16, name string) string {
	if prefix, ok := xattrPrefixes[typ&0xFF]; ok {
		return prefix + name
	}
	return fmt.Sprintf("unknown_%d.%s", typ, name)
}

// XattrFS is an optional interface that an fs.FS can implement to provide
// extended attribute access.
type XattrFS interface {
	fs.FS
	ListXattr(path string) ([]string, error)
	GetXattr(path, name string) ([]byte, error)
}
