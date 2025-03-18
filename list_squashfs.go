// +build ignore

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/KarpelesLab/squashfs"
)

func walk(fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		p := path.Join(dir, entry.Name())
		fmt.Println(p)
		if entry.IsDir() {
			err = walk(fsys, p)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run list_squashfs.go <squashfs_file>")
		os.Exit(1)
	}

	sqfs, err := squashfs.Open(os.Args[1])
	if err != nil {
		fmt.Printf("Error opening file: %s\n", err)
		os.Exit(1)
	}
	defer sqfs.Close()

	err = walk(sqfs, ".")
	if err != nil {
		fmt.Printf("Error walking filesystem: %s\n", err)
		os.Exit(1)
	}
}