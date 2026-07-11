// Package mvd provides an events.Source backed by a recorded MVD demo
// file or an in-memory MVD byte stream. It is the reference source
// implementation; analytics code should import this package only at
// wiring points (main functions, WASM entry) and otherwise work against
// the events.Source interface so alternative sources (e.g. live QTV)
// are drop-in replaceable.
package mvd

import (
	"io"
	"os"

	"github.com/mvd-analyzer/mvd-reader/events"
	"github.com/mvd-analyzer/mvd-reader/mvd"
	"github.com/mvd-analyzer/mvd-reader/mvdfile"
	"github.com/mvd-analyzer/mvd-reader/parser"
)

// Source is an events.Source implementation that pulls events from an
// MVD file or byte stream. Satisfies events.Source.
//
// Internally, the push-style parser emits into a small reset-and-reuse
// slice of events buffered between ParseOne calls. Most ParseOne
// invocations emit 0–4 events (one demo message may carry multiple
// svc_* commands), so the buffer lives on the stack-allocated initial
// backing array in the common case and never grows. `head` tracks the
// read cursor; when the consumer drains to the end we reset to index 0
// and reuse the same backing array for the next batch — crucial to
// avoid per-event allocations along the hot path.
type Source struct {
	closer  io.Closer
	decoder *mvd.Decoder
	parser  *parser.Parser
	queue   []events.Event
	head    int
	done    bool
	pendErr error // non-EOF ParseOne error, surfaced after its queued events drain
}

// Open opens an MVD file by path. Handles gzip-compressed `.mvd.gz`
// automatically. The returned Source must be Closed by the caller.
func Open(path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rc, err := mvdfile.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	// Chain both closers: the gzip ReadCloser owns decompression state,
	// the underlying os.File owns the FD. Close both to release both.
	return newSource(rc, chainCloser{rc: rc, file: f}), nil
}

// NewFromReader wraps an arbitrary io.Reader carrying an MVD byte stream
// (plain or gzipped) into a Source. The caller owns the underlying reader;
// Close on this Source only releases internal decompression state.
func NewFromReader(r io.Reader) (*Source, error) {
	rc, err := mvdfile.NewReader(r)
	if err != nil {
		return nil, err
	}
	return newSource(rc, rc), nil
}

func newSource(r io.Reader, closer io.Closer) *Source {
	dec := mvd.NewDecoder(r)
	p := parser.NewParser(dec)
	src := &Source{closer: closer, decoder: dec, parser: p}
	p.OnEvent(func(e parser.Event) error {
		src.queue = append(src.queue, e)
		return nil
	})
	return src
}

// Next pulls the next event from the stream. Returns io.EOF at a clean
// end of demo (the svc_disconnect "EndOfDemo" termination as well as a
// stream that simply runs out). A non-EOF error means the demo was
// truncated or corrupt; it is surfaced only AFTER the events the final
// ParseOne already queued have been drained, so a consumer still sees the
// tail of a broken demo before the error.
func (s *Source) Next() (events.Event, error) {
	for s.head >= len(s.queue) && !s.done && s.pendErr == nil {
		// Buffer drained; reset the read cursor and the slice length so
		// the next ParseOne's append calls reuse the existing backing
		// array instead of allocating a fresh one.
		s.queue = s.queue[:0]
		s.head = 0
		if err := s.parser.ParseOne(); err != nil {
			if err == io.EOF {
				s.done = true
			} else {
				// Stash rather than return: the message that failed may
				// have emitted valid events before the break, and those
				// were already appended to the queue above.
				s.pendErr = err
			}
			break
		}
	}
	if s.head < len(s.queue) {
		e := s.queue[s.head]
		s.queue[s.head] = nil // drop the reference so the event can be GC'd
		s.head++
		return e, nil
	}
	if s.pendErr != nil {
		err := s.pendErr
		s.pendErr = nil
		s.done = true // subsequent calls report a clean end
		return nil, err
	}
	return nil, io.EOF
}

// Close releases any resources held by the source (file handles, gzip
// state). Safe to call multiple times.
func (s *Source) Close() error {
	if s.closer != nil {
		err := s.closer.Close()
		s.closer = nil
		return err
	}
	return nil
}

// CurrentTime reports the decoder's current demo time. Useful when
// finalizing an analysis that needs the total stream duration.
func (s *Source) CurrentTime() float64 {
	return s.decoder.CurrentTime()
}

// CurrentTimeMs reports the decoder's exact wire-native demo time. Prefer it
// over CurrentTime when an inclusive/exclusive evidence boundary must be
// compared with schema timestamps, which are also stored in milliseconds.
func (s *Source) CurrentTimeMs() int32 {
	return s.decoder.CurrentTimeMs()
}

// Parser returns the underlying parser. Exposed for diagnostic tooling
// that needs to flip the parser into diagnostic mode or read collected
// warnings; not part of the stable Source contract.
func (s *Source) Parser() *parser.Parser {
	return s.parser
}

// chainCloser closes the decompressor wrapper and the underlying file in
// order, returning the first non-nil error so callers can spot trouble.
type chainCloser struct {
	rc   io.Closer
	file io.Closer
}

func (c chainCloser) Close() error {
	errRC := c.rc.Close()
	errF := c.file.Close()
	if errRC != nil {
		return errRC
	}
	return errF
}
