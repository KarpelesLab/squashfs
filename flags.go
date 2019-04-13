package squashfs

import "strings"

type SquashFlags uint16

const (
	UNCOMPRESSED_INODES SquashFlags = 1 << iota
	UNCOMPRESSED_DATA
	CHECK
	UNCOMPRESSED_FRAGMENTS
	NO_FRAGMENTS
	ALWAYS_FRAGMENTS
	DUPLICATES
	EXPORTABLE
	UNCOMPRESSED_XATTRS
	NO_XATTRS
	COMPRESSOR_OPTIONS
	UNCOMPRESSED_IDS
)

func (f SquashFlags) String() string {
	var opt []string

	if f&UNCOMPRESSED_INODES != 0 {
		opt = append(opt, "UNCOMPRESSED_INODES")
	}
	if f&UNCOMPRESSED_DATA != 0 {
		opt = append(opt, "UNCOMPRESSED_DATA")
	}
	if f&CHECK != 0 {
		opt = append(opt, "CHECK")
	}
	if f&UNCOMPRESSED_FRAGMENTS != 0 {
		opt = append(opt, "UNCOMPRESSED_FRAGMENTS")
	}
	if f&NO_FRAGMENTS != 0 {
		opt = append(opt, "NO_FRAGMENTS")
	}
	if f&ALWAYS_FRAGMENTS != 0 {
		opt = append(opt, "ALWAYS_FRAGMENTS")
	}
	if f&DUPLICATES != 0 {
		opt = append(opt, "DUPLICATES")
	}
	if f&EXPORTABLE != 0 {
		opt = append(opt, "EXPORTABLE")
	}
	if f&UNCOMPRESSED_XATTRS != 0 {
		opt = append(opt, "UNCOMPRESSED_XATTRS")
	}
	if f&NO_XATTRS != 0 {
		opt = append(opt, "NO_XATTRS")
	}
	if f&COMPRESSOR_OPTIONS != 0 {
		opt = append(opt, "COMPRESSOR_OPTIONS")
	}
	if f&UNCOMPRESSED_IDS != 0 {
		opt = append(opt, "UNCOMPRESSED_IDS")
	}

	return strings.Join(opt, "|")
}

func (f SquashFlags) Has(what SquashFlags) bool {
	return f&what == what
}
