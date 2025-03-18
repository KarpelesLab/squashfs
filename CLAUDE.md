# CLAUDE.md - SquashFS Go Project Guidelines

## Build/Test Commands
- Build: `go build -v`
- Run tests: `go test -v`
- Run single test: `go test -v -run TestName`
- Format code: `goimports -w -l .`
- Build with tags: `go build -tags "fuse xz zstd"`

## Code Style Guidelines
- Go version: 1.18+
- Error handling: Use the defined error variables in errors.go
- Naming: Follow Go conventions (CamelCase for exported, camelCase for private)
- Comments: Document exported functions with standard Go comment format
- Imports: Group standard library, then third-party packages, then local packages
- File organization: Related functionality in separate files (comp.go, comp_xz.go, etc.)
- Compression support: Use build tags (xz, zstd, fuse) to control feature inclusion
- Error formatting: Use fmt.Errorf for dynamic errors, errors.New for static ones
- Testing: Write tests in *_test.go files with appropriate test data in testdata/