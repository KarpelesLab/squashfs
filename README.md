[![GoDoc](https://godoc.org/github.com/KarpelesLab/squashfs?status.svg)](https://godoc.org/github.com/KarpelesLab/squashfs)

# squashfs

This is a read-only implementation of squashfs initially meant to be use with [go-fuse](https://github.com/hanwen/go-fuse/).

Since then, golang added `io/fs` and fuse support was moved to a `fuse` tag, which means this module can be either used with go-fuse, or as a simple `io/fs`-compliant squashfs file reader.

# Example use

```go
sqfs, err := squashfs.Open("file.squashfs")
if err != nil {
	return err
}
defer f.Close()
// sqfs can be used as a regular fs.FS
data, err := fs.ReadFile(sqfs, "dir/file.txt")
// or:
http.Handle("/", http.FileServer(sqfs))
// etc...
```

# File format

## SquashFS format

Some documentation is available online on SquashFS.

https://dr-emann.github.io/squashfs/

### Files

File data on disk is stored in blocks, which compressed size is stored as an array.

Some blocks have a size recorded as 0x1001000 when blocksize=4096, 0x1000000 means "no compress"

### Directories

It turns out no documentation is available for directory tables.

SquashFS documentation starts with:

	Directories are organised in a slightly complex way, and are not simply
	a list of file names.

It seems likely no documentation is available out there (pending further research).

Looks like "basic dir" has no index. Extended dirs seem to have an index, see index_count

It also looks like directories must be sorted, this probably has something to do with the index.
