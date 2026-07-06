// Package mvdfile provides file handling for MVD demo files with compression support.
package mvdfile

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
)

// File represents an opened MVD file that may be compressed
type File struct {
	file       *os.File
	gzipReader *gzip.Reader
	reader     io.Reader
}

// Open opens an MVD file, automatically detecting and handling gzip compression.
// Files that begin with the gzip magic bytes (0x1f 0x8b) are decompressed;
// the filename suffix is not consulted.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	bufReader, isGzip := peekGzip(f)
	if isGzip {
		gzReader, err := gzip.NewReader(bufReader)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &File{
			file:       f,
			gzipReader: gzReader,
			reader:     gzReader,
		}, nil
	}

	// Raw MVD file
	return &File{
		file:   f,
		reader: bufReader,
	}, nil
}

// peekGzip wraps r in a buffered reader and reports whether the stream
// begins with the gzip magic bytes (0x1f 0x8b). A stream too short to
// peek two bytes is reported as not gzipped. The returned reader has not
// consumed the magic bytes, so it is safe to pass to gzip.NewReader.
func peekGzip(r io.Reader) (*bufio.Reader, bool) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil {
		return br, false
	}
	return br, magic[0] == 0x1f && magic[1] == 0x8b
}

// Read implements io.Reader
func (f *File) Read(p []byte) (n int, err error) {
	return f.reader.Read(p)
}

// Close closes the file and any decompression readers
func (f *File) Close() error {
	if f.gzipReader != nil {
		f.gzipReader.Close()
	}
	return f.file.Close()
}

// Name returns the original file path
func (f *File) Name() string {
	return f.file.Name()
}

// IsCompressed returns true if the file is gzip compressed
func (f *File) IsCompressed() bool {
	return f.gzipReader != nil
}

// NewReader wraps an io.Reader with automatic gzip detection.
// If the stream starts with gzip magic bytes (0x1f 0x8b), it returns a gzip reader.
// Otherwise, it returns the original stream. The caller must close the returned ReadCloser.
func NewReader(r io.Reader) (io.ReadCloser, error) {
	bufReader, isGzip := peekGzip(r)
	if isGzip {
		gzReader, err := gzip.NewReader(bufReader)
		if err != nil {
			return nil, err
		}
		return gzReader, nil
	}
	return io.NopCloser(bufReader), nil
}
