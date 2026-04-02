package squashfs

// devIno uniquely identifies a file on a device for hard link detection.
type devIno struct {
	dev uint64
	ino uint64
}
