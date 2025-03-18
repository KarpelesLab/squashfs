module github.com/KarpelesLab/squashfs/cmd/sqfs

go 1.23.1

require github.com/KarpelesLab/squashfs v0.0.0

require (
	github.com/hanwen/go-fuse/v2 v2.1.0 // indirect
	github.com/klauspost/compress v1.15.12 // indirect
	github.com/ulikunitz/xz v0.5.10 // indirect
	golang.org/x/sys v0.0.0-20180830151530-49385e6e1522 // indirect
)

replace github.com/KarpelesLab/squashfs => ../..
