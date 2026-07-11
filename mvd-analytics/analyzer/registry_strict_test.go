package analyzer

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

type sourceResult struct {
	event events.Event
	err   error
}

type scriptedSource struct {
	results []sourceResult
	next    int
}

func (s *scriptedSource) Next() (events.Event, error) {
	if s.next >= len(s.results) {
		return nil, io.EOF
	}
	result := s.results[s.next]
	s.next++
	return result.event, result.err
}

func (*scriptedSource) Close() error { return nil }

func TestAnalyzeSourceStrictRejectsDecodeErrorAfterEvents(t *testing.T) {
	decodeErr := errors.New("sentinel decode failure")
	source := &scriptedSource{results: []sourceResult{
		{event: &events.PrintEvent{}},
		{err: decodeErr},
	}}

	got, err := NewRegistry().AnalyzeSourceStrict(source, "truncated.mvd")

	if !errors.Is(err, decodeErr) {
		t.Fatalf("error = %v, want wrapped sentinel decode failure", err)
	}
	if got != nil {
		t.Fatalf("result = %+v, want nil on strict source failure", got)
	}
}

func TestAnalyzeSourceKeepsLegacyPartialResultOnDecodeError(t *testing.T) {
	decodeErr := errors.New("sentinel decode failure")
	source := &scriptedSource{results: []sourceResult{
		{event: &events.PrintEvent{}},
		{err: decodeErr},
	}}

	got, err := NewRegistry().AnalyzeSource(source, "partial.mvd")

	if err != nil {
		t.Fatalf("AnalyzeSource error = %v, want legacy partial-result success", err)
	}
	if got == nil || got.FilePath != "partial.mvd" {
		t.Fatalf("result = %+v, want partial result with original filename", got)
	}
}

type finalizeErrorAnalyzer struct {
	err error
}

func (*finalizeErrorAnalyzer) Name() string               { return "finalize-error" }
func (*finalizeErrorAnalyzer) Init(*Context) error        { return nil }
func (*finalizeErrorAnalyzer) OnEvent(events.Event) error { return nil }
func (a *finalizeErrorAnalyzer) Finalize(*Result) error   { return a.err }

func TestAnalyzeSourceStrictRejectsFinalizationErrors(t *testing.T) {
	finalizeErr := errors.New("sentinel finalize failure")
	registry := NewRegistry()
	registry.RegisterDerived(&finalizeErrorAnalyzer{err: finalizeErr})
	source := &scriptedSource{results: []sourceResult{
		{event: &events.PrintEvent{}},
	}}

	got, err := registry.AnalyzeSourceStrict(source, "invalid.mvd")

	if err == nil || !strings.Contains(err.Error(), finalizeErr.Error()) {
		t.Fatalf("error = %v, want finalization failure", err)
	}
	if got != nil {
		t.Fatalf("result = %+v, want nil when strict analysis records errors", got)
	}
}
