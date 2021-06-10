package jaeger

import (
	"context"
	"sync"

	"github.com/moby/buildkit/util/tracing/detect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const maxBuffer = 128

var exp = &Exporter{}

func init() {
	detect.Register("delegated", func() (sdktrace.SpanExporter, error) {
		return exp, nil
	})
}

func ExporterFromContext(ctx context.Context) *Exporter {
	s := trace.SpanFromContext(ctx)
	if s == nil {
		return nil
	}
	t := s.Tracer()
	if te, ok := t.(interface {
		SpanExporter() sdktrace.SpanExporter
	}); ok {
		if exp, ok := te.SpanExporter().(*Exporter); ok {
			return exp
		}
	}
	return nil
}

type Exporter struct {
	mu       sync.Mutex
	delegate sdktrace.SpanExporter
	buffer   []*sdktrace.SpanSnapshot
}

var _ sdktrace.SpanExporter = &Exporter{}

func (e *Exporter) ExportSpans(ctx context.Context, ss []*sdktrace.SpanSnapshot) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.delegate != nil {
		return e.delegate.ExportSpans(ctx, ss)
	}

	if len(e.buffer) > maxBuffer {
		return nil
	}

	e.buffer = append(e.buffer, ss...)
	return nil
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.delegate != nil {
		return e.delegate.Shutdown(ctx)
	}

	return nil
}

func (e *Exporter) SetDelegate(ctx context.Context, del sdktrace.SpanExporter) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.delegate = del

	if len(e.buffer) > 0 {
		return e.delegate.ExportSpans(ctx, e.buffer)
	}
	return nil
}
