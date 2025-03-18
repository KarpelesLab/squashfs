package main

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/KarpelesLab/squashfs"
)

const usage = `sqfs - SquashFS CLI tool

Usage:
  sqfs ls <squashfs_file> [<path>]          List files in SquashFS (optionally in a specific path)
  sqfs cat <squashfs_file> <file>           Display contents of a file in SquashFS
  sqfs help                                 Show this help message

Examples:
  sqfs ls archive.squashfs                  List all files at the root of archive.squashfs
  sqfs ls archive.squashfs lib              List all files in the lib directory
  sqfs cat archive.squashfs dir/file.txt    Display contents of file.txt from archive.squashfs
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