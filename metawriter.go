package squashfs

import (
	"bytes"
	"encoding/binary"
	"io"
)

const (
	metaBlockSize    = 8192
	metaBlockHeader  = 2
	uncompressedFlag = 0x8000
)

// streamingMetaWriter writes metadata blocks directly to output as they fill up.
// Physical block positions are known immediately after each block is written.
type streamingMetaWriter struct {
	w       io.Writer
	comp    Compression
	current *bytes.Buffer
	offset  uint64 // Current write position (physical offset from start of this table)

	// Track physical position of each block for later reference
	blockOffsets []uint64
}

func newStreamingMetaWriter(w io.Writer, comp Compression) *streamingMetaWriter {
	return &streamingMetaWriter{
		w:            w,
		comp:         comp,
		current:      bytes.NewBuffer(make([]byte, 0, metaBlockSize)),
		offset:       0,
		blockOffsets: make([]uint64, 0),
	}
}

// Position returns the current position as (blockIndex, offsetWithinBlock).
// The physical byte offset can be looked up via BlockOffset(blockIndex).
func (m *streamingMetaWriter) Position() (blockIdx uint32, offset uint16) {
	// If current block is full, the next write will trigger a flush,
	// so the actual position will be at the start of the next block.
	if m.current.Len() == metaBlockSize {
		return uint32(len(m.blockOffsets) + 1), 0
	}
	return uint32(len(m.blockOffsets)), uint16(m.current.Len())
}

// BlockOffset returns the physical byte offset for a given block index.
// Only valid for blocks that have already been flushed.
func (m *streamingMetaWriter) BlockOffset(blockIdx uint32) uint64 {
	if int(blockIdx) < len(m.blockOffsets) {
		return m.blockOffsets[blockIdx]
	}
	// For the current block (not yet flushed), return current offset
	return m.offset
}

// Write appends data to the metadata stream, flushing blocks as needed.
func (m *streamingMetaWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		remaining := metaBlockSize - m.current.Len()
		if remaining == 0 {
			// Current block is full, flush it
			if err := m.flushBlock(); err != nil {
				return written, err
			}
			remaining = metaBlockSize
		}

		toWrite := len(data)
		if toWrite > remaining {
			toWrite = remaining
		}

		n, err := m.current.Write(data[:toWrite])
		if err != nil {
			return written, err
		}
		written += n
		data = data[toWrite:]
	}
	return written, nil
}

// flushBlock compresses and writes the current block to output.
func (m *streamingMetaWriter) flushBlock() error {
	if m.current.Len() == 0 {
		return nil
	}

	// Record physical offset before writing
	m.blockOffsets = append(m.blockOffsets, m.offset)

	blockData := m.current.Bytes()
	compressed, err := m.comp.compress(blockData)

	var header uint16
	var outData []byte
	if err != nil || compressed == nil || len(compressed) >= len(blockData) {
		// Use uncompressed
		header = uint16(len(blockData)) | uncompressedFlag
		outData = blockData
	} else {
		// Use compressed
		header = uint16(len(compressed))
		outData = compressed
	}

	// Write header
	headerBuf := make([]byte, 2)
	binary.LittleEndian.PutUint16(headerBuf, header)
	if _, err := m.w.Write(headerBuf); err != nil {
		return err
	}
	m.offset += 2

	// Write data
	if _, err := m.w.Write(outData); err != nil {
		return err
	}
	m.offset += uint64(len(outData))

	// Reset current buffer
	m.current = bytes.NewBuffer(make([]byte, 0, metaBlockSize))
	return nil
}

// Flush writes any remaining data in the current block.
func (m *streamingMetaWriter) Flush() error {
	if m.current.Len() > 0 {
		return m.flushBlock()
	}
	return nil
}

// TotalSize returns the total bytes written so far.
func (m *streamingMetaWriter) TotalSize() uint64 {
	return m.offset
}

// bufferedMetaWriter buffers metadata in memory but compresses blocks as they fill.
// This allows us to know the physical byte offset at any time, even before writing to disk.
// Used for the directory table which needs to be written after the inode table.
type bufferedMetaWriter struct {
	comp Compression

	// Compressed blocks ready for output
	compressedBlocks [][]byte // Each entry is header + compressed data

	// Current block being built (uncompressed)
	current *bytes.Buffer

	// Cumulative physical size of completed blocks
	physicalOffset uint64
}

func newBufferedMetaWriter(comp Compression) *bufferedMetaWriter {
	return &bufferedMetaWriter{
		comp:             comp,
		compressedBlocks: make([][]byte, 0),
		current:          bytes.NewBuffer(make([]byte, 0, metaBlockSize)),
		physicalOffset:   0,
	}
}

// Position returns the current position as (physicalByteOffset, offsetWithinBlock).
// The physical byte offset is deterministic because blocks are compressed when full.
// If the current block is full, it will be flushed to determine the correct physical offset.
func (m *bufferedMetaWriter) Position() (physOffset uint64, offset uint16) {
	// If current block is full, the next write will trigger a flush.
	// We need to flush now to know the correct physical offset for the next write.
	if m.current.Len() == metaBlockSize {
		// flushBlock only fails on compression errors which shouldn't happen
		// for valid data. If it does fail, physicalOffset will be stale.
		_ = m.flushBlock()
	}
	return m.physicalOffset, uint16(m.current.Len())
}

// Write appends data to the metadata stream, compressing blocks as they fill.
func (m *bufferedMetaWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		remaining := metaBlockSize - m.current.Len()
		if remaining == 0 {
			// Current block is full, compress and store it
			if err := m.flushBlock(); err != nil {
				return written, err
			}
			remaining = metaBlockSize
		}

		toWrite := len(data)
		if toWrite > remaining {
			toWrite = remaining
		}

		n, err := m.current.Write(data[:toWrite])
		if err != nil {
			return written, err
		}
		written += n
		data = data[toWrite:]
	}
	return written, nil
}

// flushBlock compresses the current block and adds it to the buffer.
func (m *bufferedMetaWriter) flushBlock() error {
	if m.current.Len() == 0 {
		return nil
	}

	blockData := m.current.Bytes()
	compressed, err := m.comp.compress(blockData)

	var header uint16
	var outData []byte
	if err != nil || compressed == nil || len(compressed) >= len(blockData) {
		header = uint16(len(blockData)) | uncompressedFlag
		outData = make([]byte, len(blockData))
		copy(outData, blockData)
	} else {
		header = uint16(len(compressed))
		outData = compressed
	}

	// Create block with header + data
	block := make([]byte, 2+len(outData))
	binary.LittleEndian.PutUint16(block, header)
	copy(block[2:], outData)

	m.compressedBlocks = append(m.compressedBlocks, block)
	m.physicalOffset += uint64(len(block))

	// Reset current buffer
	m.current = bytes.NewBuffer(make([]byte, 0, metaBlockSize))
	return nil
}

// WriteToOutput writes all buffered blocks to the output.
// Returns the total size written.
func (m *bufferedMetaWriter) WriteToOutput(w io.Writer) (uint64, error) {
	// Flush any remaining data
	if m.current.Len() > 0 {
		if err := m.flushBlock(); err != nil {
			return 0, err
		}
	}

	var totalSize uint64
	for _, block := range m.compressedBlocks {
		if _, err := w.Write(block); err != nil {
			return totalSize, err
		}
		totalSize += uint64(len(block))
	}

	return totalSize, nil
}
