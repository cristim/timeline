package ingest

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
)

// Wikidata publishes its JSON dump as latest-all.json.bz2 (SRC-1 tier 1, held
// in the COLD tier per SRC-3). Decompressing it needs no dependency and no
// external process: compress/bzip2 and compress/gzip are stdlib, both stream,
// and both handle the concatenated members real dumps sometimes carry.
// Decompression itself is O(1) in the archive's size; what a caller keeps from
// the decoded stream is the caller's business.

type DumpCompression string

const (
	DumpCompressionNone  DumpCompression = "none"
	DumpCompressionGzip  DumpCompression = "gzip"
	DumpCompressionBzip2 DumpCompression = "bzip2"
)

var (
	gzipMagic  = []byte{0x1f, 0x8b}
	bzip2Magic = []byte("BZh")
)

// OpenWikidataDumpStream sniffs the container and returns the decoded JSON
// stream. Detection is by magic bytes, not by file name, so a dump piped in on
// stdin works the same as one named on the command line.
func OpenWikidataDumpStream(r io.Reader) (io.Reader, DumpCompression, error) {
	if r == nil {
		return nil, "", fmt.Errorf("open wikidata dump stream: nil reader")
	}
	buffered := bufio.NewReader(r)
	magic, err := buffered.Peek(len(bzip2Magic))
	if err != nil && err != io.EOF {
		return nil, "", fmt.Errorf("open wikidata dump stream: read magic: %w", err)
	}

	switch {
	case bytes.HasPrefix(magic, gzipMagic):
		decoded, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, "", fmt.Errorf("open wikidata dump stream: gzip: %w", err)
		}
		return decoded, DumpCompressionGzip, nil
	case bytes.HasPrefix(magic, bzip2Magic):
		return bzip2.NewReader(buffered), DumpCompressionBzip2, nil
	default:
		return buffered, DumpCompressionNone, nil
	}
}
