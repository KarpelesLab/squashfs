[![Go Report Card](https://goreportcard.com/badge/github.com/KarpelesLab/squashfs?style=flat-square)](https://goreportcard.com/report/github.com/KarpelesLab/squashfs)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/KarpelesLab/squashfs)](https://pkg.go.dev/github.com/KarpelesLab/squashfs)
[![Coverage Status](https://coveralls.io/repos/github/KarpelesLab/squashfs/badge.svg?branch=master)](https://coveralls.io/github/KarpelesLab/squashfs?branch=master)
[![Tags](https://img.shields.io/github/tag/KarpelesLab/squashfs.svg?style=flat-square)](https://github.com/KarpelesLab/squashfs/tags)

# squashfs

A pure Go implementation of SquashFS, supporting both reading and writing of squashfs filesystem images.

The read path implements Go's `io/fs` interface and optionally supports FUSE via the `fuse` build tag. The write path can create squashfs images from an `fs.FS` or programmatically, with support for all file types (regular files, directories, symlinks, devices, fifos, sockets) and compression formats.

## Tags

The following tags can be specified on build to enable/disable features:

* `fuse` adds methods to the Inode object to interact with fuse
* `xz` adds a dependency on github.com/ulikunitz/xz to support XZ compressed files
* `zstd` adds a dependency on github.com/klauspost/compress/zstd to support ZSTD compressed files

By default, only GZip compression is supported as to limit dependency on external libraries. The following can be easily enabled by adding a single line before any call to the library:

### zstd

```go
import "github.com/klauspost/compress/zstd"

func init() {
    squashfs.RegisterCompHandler(squashfs.ZSTD, &squashfs.CompHandler{
        Decompress: squashfs.MakeDecompressor(zstd.ZipDecompressor()),
    })
}
```

### xz

```go
import (
    "io"

    "github.com/ulikunitz/xz"
)

func init() {
    squashfs.RegisterCompHandler(squashfs.XZ, &squashfs.CompHandler{
        Decompress: squashfs.MakeDecompressorErr(func(r io.Reader) (io.ReadCloser, error) {
            rc, err := xz.NewReader(r)
            if err != nil {
                return nil, err
            }
            return io.NopCloser(rc), nil
        }),
    })
}
```

### Others

LZMA, LZO and LZ4 are also defined by squashfs and can be enabled similarly.

# Example usage

## Reading a squashfs image

```go
sqfs, err := squashfs.Open("file.squashfs")
if err != nil {
    return err
}
defer sqfs.Close()

// sqfs implements fs.FS, fs.ReadDirFS, and fs.StatFS
data, err := fs.ReadFile(sqfs, "dir/file.txt")

// Serve files over HTTP
http.Handle("/", http.FileServer(sqfs))

// List directory contents
entries, err := sqfs.ReadDir("some/directory")

// Read a symlink target
target, err := sqfs.Readlink("path/to/symlink")
```

## Creating a squashfs image from a directory

```go
out, err := os.Create("output.squashfs")
if err != nil {
    return err
}
defer out.Close()

w, err := squashfs.NewWriter(out)
if err != nil {
    return err
}

// Add all files from an existing directory
if err := w.AddFS(os.DirFS("/path/to/source")); err != nil {
    return err
}

return w.Finalize()
```

## Creating a squashfs image programmatically

```go
w, err := squashfs.NewWriter(out,
    squashfs.WithCompression(squashfs.ZSTD),
    squashfs.WithBlockSize(262144),
)
if err != nil {
    return err
}

// Regular files
w.AddFile("etc/config.json", configData, 0644)
w.SetOwner("etc/config.json", 0, 0)

// Directories
w.AddDirectory("var/log", 0755)

// Symlinks
w.AddSymlink("usr/lib64", "lib")

// Device nodes
w.AddDevice("dev/null", fs.ModeCharDevice|0666, 1<<8|3)

// Named pipes and sockets
w.AddFifo("run/myapp.pipe", 0600)
w.AddSocket("run/myapp.sock", 0600)

return w.Finalize()
```

# File format

Some documentation is available online on SquashFS.

* https://dr-emann.github.io/squashfs/
* https://dr-emann.github.io/squashfs/squashfs.html

# Features

* Read and write squashfs filesystem images in pure Go
* Read path implements `fs.FS`, `fs.ReadDirFS`, and `fs.StatFS`
* Write path supports all inode types: regular files, directories, symlinks, block/char devices, fifos, sockets
* Full metadata support: permissions (including setuid/setgid/sticky), uid/gid, timestamps
* GZip compression by default, with ZSTD and XZ available via build tags
* Extensible compression through the `RegisterCompHandler` API
* Optional FUSE support via the `fuse` build tag
* Directory index support for fast lookups in large directories
* Writer output validated against the reference `unsquashfs` implementation
* CLI tool for exploring and extracting files from squashfs archives

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

# Limitations

The writer does not currently support:

* Fragment tables (tail-end packing of small files)
* NFS export tables
* File deduplication
* Extended attributes (xattrs)
