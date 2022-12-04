# SquashFS format

Some documentation is available online on SquashFS.

https://dr-emann.github.io/squashfs/

## Files

File data on disk is stored in blocks, which compressed size is stored as an array.

Some blocks have a size recorded as 0x1001000 when blocksize=4096, 0x1000000 probably means "no compress"

## Directories

It turns out no documentation is available for directory tables.

SquashFS documentation starts with:

	Directories are organised in a slightly complex way, and are not simply
	a list of file names.

It seems likely no documentation is available out there (pending further research).

Looks like "basic dir" has no index. Extended dirs seem to have an index, see index_count
