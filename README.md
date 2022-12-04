[![GoDoc](https://godoc.org/github.com/KarpelesLab/squashfs?status.svg)](https://godoc.org/github.com/KarpelesLab/squashfs)

# squashfs

This is a read-only implementation of squashfs initially meant to be use with [go-fuse](https://github.com/hanwen/go-fuse/).

Since then, golang added `io/fs` and fuse support was moved to a `fuse` tag, which means this module can be either used with go-fuse, or as a simple `io/fs`-compliant squashfs file reader.

## Tags

The following tags can be specified on build to enable/disable features:

* `fuse` adds methods to the Inode object to interact with fuse
* `xz` adds a dependency on xz to support xz compressed files
* `zstd` adds a dependency on zstd to support zstd compressed files

# Example use

```go
sqfs, err := squashfs.Open("file.squashfs")
if err != nil {
	return err
}
defer sqfs.Close()
// sqfs can be used as a regular fs.FS
data, err := fs.ReadFile(sqfs, "dir/file.txt")
// or:
http.Handle("/", http.FileServer(sqfs))
// etc...
```

You can find more looking at the test file.

# File format

Some documentation is available online on SquashFS.

* https://dr-emann.github.io/squashfs/
* https://dr-emann.github.io/squashfs/squashfs.html

# TODO

Access to directories do not currently use indexes and can be slow for random file accesses in very large directories.
