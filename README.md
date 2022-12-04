# squashfs

This is a read-only implementation of squashfs initially meant to be use with [go-fuse](https://github.com/hanwen/go-fuse/).

Since then, golang added `io/fs` and fuse support was moved to a `fuse` tag, which means this module can be either used with go-fuse, or as a simple `io/fs`-compliant squashfs file reader.


