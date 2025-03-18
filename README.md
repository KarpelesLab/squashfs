[![GoDoc](https://godoc.org/github.com/KarpelesLab/squashfs?status.svg)](https://godoc.org/github.com/KarpelesLab/squashfs)

# squashfs

This is a read-only implementation of squashfs initially meant to be use with [go-fuse](https://github.com/hanwen/go-fuse/).

Since then, golang added `io/fs` and fuse support was moved to a `fuse` tag, which means this module can be either used with go-fuse, or as a simple `io/fs`-compliant squashfs file reader.

## Tags

The following tags can be specified on build to enable/disable features:

* `fuse` adds methods to the Inode object to interact with fuse
* `xz` adds a dependency on xz to support xz compressed files
* `zstd` adds a dependency on zstd to support zstd compressed files

# Example usage

## Basic file access

```go
sqfs, err := squashfs.Open("file.squashfs")
if err != nil {
	return err
}
defer sqfs.Close()

// sqfs can be used as a regular fs.FS
data, err := fs.ReadFile(sqfs, "dir/file.txt")
if err != nil {
	return err
}

// Or serve files over HTTP
http.Handle("/", http.FileServer(sqfs))
```

## Reading directories

```go
// List directory contents
entries, err := sqfs.ReadDir("some/directory")
if err != nil {
	return err
}

// Process directory entries
for _, entry := range entries {
	fmt.Printf("Name: %s, IsDir: %v\n", entry.Name(), entry.IsDir())
	
	// Get more info if needed
	info, err := entry.Info()
	if err != nil {
		return err
	}
	fmt.Printf("Size: %d, Mode: %s\n", info.Size(), info.Mode())
}
```

## Reading symlinks

```go
// Read a symlink target
target, err := sqfs.Readlink("path/to/symlink")
if err != nil {
	return err
}
fmt.Printf("Symlink points to: %s\n", target)
```

## Custom compression support

```go
// Register XZ support (requires "xz" build tag)
import (
	"github.com/KarpelesLab/squashfs"
	"github.com/ulikunitz/xz"
)

// Register XZ decompressor at init time
func init() {
	squashfs.RegisterDecompressor(squashfs.XZ, squashfs.MakeDecompressorErr(xz.NewReader))
}
```

For more examples, see the test files in the project.

# File format

Some documentation is available online on SquashFS.

* https://dr-emann.github.io/squashfs/
* https://dr-emann.github.io/squashfs/squashfs.html

# Features

* Read-only implementation of squashfs compatible with Go's `io/fs` interface
* Optional FUSE support with the `fuse` build tag
* Support for various compression formats through build tags
* Directory index support for fast access to files in large directories
* Symlink support

# Performance

As of November 2024, directory indexes are now used for efficient file lookup in large directories, significantly improving performance for random file access.
