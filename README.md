[![Go Report Card](https://goreportcard.com/badge/github.com/KarpelesLab/squashfs?style=flat-square)](https://goreportcard.com/report/github.com/KarpelesLab/squashfs)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/KarpelesLab/squashfs)](https://pkg.go.dev/github.com/KarpelesLab/squashfs)
[![Tags](https://img.shields.io/github/tag/KarpelesLab/squashfs.svg?style=flat-square)](https://github.com/KarpelesLab/squashfs/tags)

# squashfs

This is a read-only implementation of squashfs initially meant to be use with [go-fuse](https://github.com/hanwen/go-fuse/).

Since then, golang added `io/fs` and fuse support was moved to a `fuse` tag, which means this module can be either used with go-fuse, or as a simple `io/fs`-compliant squashfs file reader.

## Tags

The following tags can be specified on build to enable/disable features:

* `fuse` adds methods to the Inode object to interact with fuse
* `xz` adds a dependency on github.com/ulikunitz/xz to support XZ compressed files
* `zstd` adds a dependency on github.com/klauspost/compress/zstd to support ZSTD compressed files

Note: By default, only GZip compression is supported. Other compression formats mentioned in the SquashFS specification (LZMA, LZO, LZ4) are not currently implemented via build tags, but can be added by manually registering decompressors.

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
* Support for GZip compression by default, with XZ and ZSTD available via build tags
* Extensible compression support through the RegisterDecompressor API
* Directory index support for fast access to files in large directories
* Symlink support
* CLI tool for exploring and extracting files from SquashFS archives

# CLI Tool

A command-line interface tool is provided in the `cmd/sqfs` directory. This tool allows you to:

* List files in a SquashFS archive
* View the contents of files inside a SquashFS archive
* Display detailed information about SquashFS archives

## Usage

```
sqfs - SquashFS CLI tool

Usage:
  sqfs ls <squashfs_file> [<path>]          List files in SquashFS (optionally in a specific path)
  sqfs cat <squashfs_file> <file>           Display contents of a file in SquashFS
  sqfs info <squashfs_file>                 Display information about a SquashFS archive
  sqfs help                                 Show this help message

Examples:
  sqfs ls archive.squashfs                  List all files at the root of archive.squashfs
  sqfs ls archive.squashfs lib              List all files in the lib directory
  sqfs cat archive.squashfs dir/file.txt    Display contents of file.txt from archive.squashfs
  sqfs info archive.squashfs                Show metadata about the SquashFS archive
```

## Installing the CLI Tool

```
go install github.com/KarpelesLab/squashfs/cmd/sqfs@latest
```

To install with additional compression support:

```
go install -tags "xz zstd" github.com/KarpelesLab/squashfs/cmd/sqfs@latest
```

# Performance

As of November 2024, directory indexes are now used for efficient file lookup in large directories, significantly improving performance for random file access.
