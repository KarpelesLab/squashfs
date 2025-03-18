package main

import (
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/KarpelesLab/squashfs"
)

const usage = `sqfs - SquashFS CLI tool

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
`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "ls":
		if len(os.Args) < 3 {
			fmt.Println("Error: Missing SquashFS file path")
			fmt.Println(usage)
			os.Exit(1)
		}
		sqfsPath := os.Args[2]
		path := "."
		if len(os.Args) > 3 {
			path = os.Args[3]
		}
		err := listFiles(sqfsPath, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}

	case "cat":
		if len(os.Args) < 4 {
			fmt.Println("Error: Missing SquashFS file path or target file")
			fmt.Println(usage)
			os.Exit(1)
		}
		sqfsPath := os.Args[2]
		filePath := os.Args[3]
		err := catFile(sqfsPath, filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}

	case "info":
		if len(os.Args) < 3 {
			fmt.Println("Error: Missing SquashFS file path")
			fmt.Println(usage)
			os.Exit(1)
		}
		sqfsPath := os.Args[2]
		err := showInfo(sqfsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}

	case "help":
		fmt.Println(usage)

	default:
		fmt.Printf("Error: Unknown command '%s'\n", cmd)
		fmt.Println(usage)
		os.Exit(1)
	}
}

// printFileInfo prints file information in a consistent format
func printFileInfo(path string, info fs.FileInfo) {
	// Determine file type indicator
	typeChar := "-"
	if info.IsDir() {
		typeChar = "d"
	} else if info.Mode()&fs.ModeSymlink != 0 {
		typeChar = "l"
	}

	// Format permissions
	mode := info.Mode().String()
	permissions := mode[1:] // Skip the type character

	// Format size
	size := fmt.Sprintf("%8d", info.Size())
	if info.IsDir() {
		size = "       -"
	}

	// Format modification time
	timeStr := info.ModTime().Format("Jan 02 15:04")

	// Print the line
	fmt.Printf("%s%s %s %s %s\n", typeChar, permissions, size, timeStr, path)
}

// listFiles lists files in SquashFS in the specified path
func listFiles(sqfsPath, dirPath string) error {
	sqfs, err := squashfs.Open(sqfsPath)
	if err != nil {
		return fmt.Errorf("failed to open SquashFS file: %w", err)
	}
	defer sqfs.Close()

	// If the dirPath is not ".", check if it exists and is a directory
	if dirPath != "." {
		info, err := fs.Stat(sqfs, dirPath)
		if err != nil {
			return fmt.Errorf("path '%s' not found: %w", dirPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("'%s' is not a directory", dirPath)
		}
	}

	// Read the directory entries
	entries, err := fs.ReadDir(sqfs, dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory '%s': %w", dirPath, err)
	}

	// Process each entry
	for _, entry := range entries {
		// Build the display path
		var displayPath string
		if dirPath == "." {
			// Just use the entry name for root directory
			displayPath = entry.Name()
		} else {
			// Add directory prefix for subdirectories
			displayPath = dirPath + "/" + entry.Name()
		}

		// Get detailed info
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get info for '%s': %s\n", displayPath, err)
			continue
		}

		// Print file info
		printFileInfo(displayPath, info)
	}

	return nil
}

// catFile displays the contents of a file from a SquashFS archive
func catFile(sqfsPath, filePath string) error {
	sqfs, err := squashfs.Open(sqfsPath)
	if err != nil {
		return fmt.Errorf("failed to open SquashFS file: %w", err)
	}
	defer sqfs.Close()

	// Read the entire file
	data, err := fs.ReadFile(sqfs, filePath)
	if err != nil {
		return fmt.Errorf("failed to read file '%s': %w", filePath, err)
	}

	// Write the file contents to stdout
	_, err = os.Stdout.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write file contents to stdout: %w", err)
	}

	return nil
}

// showInfo displays metadata information about a SquashFS archive
func showInfo(sqfsPath string) error {
	sqfs, err := squashfs.Open(sqfsPath)
	if err != nil {
		return fmt.Errorf("failed to open SquashFS file: %w", err)
	}
	defer sqfs.Close()

	// sqfs is already a *squashfs.Superblock
	sb := sqfs

	// Format header
	fmt.Println("SquashFS Archive Information")
	fmt.Println("===========================")

	// Format creation time
	createTime := time.Unix(int64(sb.ModTime), 0)

	// Print basic information
	fmt.Printf("Version:          %d.%d\n", sb.VMajor, sb.VMinor)
	fmt.Printf("Creation time:    %s\n", createTime.Format(time.RFC1123))
	fmt.Printf("Block size:       %d bytes\n", sb.BlockSize)
	fmt.Printf("Compression:      %s\n", sb.Comp)
	fmt.Printf("Flags:            %s\n", sb.Flags)
	fmt.Printf("Total size:       %d bytes\n", sb.BytesUsed)
	fmt.Printf("Inode count:      %d\n", sb.InodeCnt)
	fmt.Printf("Fragment count:   %d\n", sb.FragCount)
	fmt.Printf("ID count:         %d\n", sb.IdCount)

	// Count files and directories
	var fileCount, dirCount, symCount int
	countFilesAndDirs(sqfs, ".", &fileCount, &dirCount, &symCount)

	fmt.Println("\nContent Summary")
	fmt.Println("--------------")
	fmt.Printf("Directories:      %d\n", dirCount)
	fmt.Printf("Regular files:    %d\n", fileCount)
	fmt.Printf("Symlinks:         %d\n", symCount)

	return nil
}

// countFilesAndDirs recursively counts files, directories and symlinks in the archive
func countFilesAndDirs(fsys fs.FS, dir string, fileCount, dirCount, symCount *int) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.IsDir() {
			*dirCount++
			// Recursively count in this directory
			subdir := dir
			if dir == "." {
				subdir = entry.Name()
			} else {
				subdir = dir + "/" + entry.Name()
			}
			countFilesAndDirs(fsys, subdir, fileCount, dirCount, symCount)
		} else if info.Mode()&fs.ModeSymlink != 0 {
			*symCount++
		} else {
			*fileCount++
		}
	}
}
