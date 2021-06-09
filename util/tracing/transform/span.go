package transform

import (
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxMessageEventsPerSpan = 128
)

// SpanData transforms slice of OTLP ResourceSpan into a slice of SpanSnapshots.
func SpanData(sdl []*tracepb.ResourceSpans) []*tracesdk.SpanSnapshot {
	if len(sdl) == 0 {
		return nil
	}

	var out []*tracesdk.SpanSnapshot

	for _, sd := range sdl {
		if sd == nil {
			continue
		}

		r := Resource(sd.Resource)

		for _, sdi := range sd.InstrumentationLibrarySpans {
			sda := make([]*tracesdk.SpanSnapshot, len(sdi.Spans))
			for i, sd := range sdi.Spans {
				s := span(sd)
				s.InstrumentationLibrary = instrumentationLibrary(sdi.InstrumentationLibrary)
				s.Resource = r
				sda[i] = s
			}
			out = append(out, sda...)
		}
	}

	return out
}

func span(sd *tracepb.Span) *tracesdk.SpanSnapshot {
	if sd == nil {
		return nil
	}

	var tid trace.TraceID
	copy(tid[:], sd.TraceId)
	var sid trace.SpanID
	copy(sid[:], sd.SpanId)
	var psid trace.SpanID
	if len(sd.ParentSpanId) > 0 {
		copy(psid[:], sd.ParentSpanId)
	}

	sctx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceState: parseTraceState(sd.TraceState),
	})

	psctx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid,
		SpanID:  psid,
	})

	s := &tracesdk.SpanSnapshot{
		SpanContext:              sctx,
		StatusCode:               statusCode(sd.Status),
		StatusMessage:            sd.Status.GetMessage(),
		StartTime:                time.Unix(0, int64(sd.StartTimeUnixNano)),
		EndTime:                  time.Unix(0, int64(sd.EndTimeUnixNano)),
		Links:                    links(sd.Links),
		SpanKind:                 spanKind(sd.Kind),
		Name:                     sd.Name,
		Attributes:               Attributes(sd.Attributes),
		MessageEvents:            spanEvents(sd.Events),
		DroppedAttributeCount:    int(sd.DroppedAttributesCount),
		DroppedMessageEventCount: int(sd.DroppedEventsCount),
		DroppedLinkCount:         int(sd.DroppedLinksCount),
		Parent:                   psctx,
	}

	return s
}

func parseTraceState(in string) trace.TraceState {
	if in == "" {
		return trace.TraceState{}
	}

	kvs := []attribute.KeyValue{}
	for _, entry := range strings.Split(in, ",") {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			// Parse failure, abort!
			return trace.TraceState{}
		}
		kvs = append(kvs, attribute.String(parts[0], parts[1]))
	}

	// Ignoring error here as "failure to parse tracestate MUST NOT
	// affect the parsing of traceparent."
	// https://www.w3.org/TR/trace-context/#tracestate-header
	ts, _ := trace.TraceStateFromKeyValues(kvs...)
	return ts
}

// status transform a OTLP span status into span code.
func statusCode(st *tracepb.Status) codes.Code {
	switch st.Code {
	case tracepb.Status_STATUS_CODE_ERROR:
		return codes.Error
	default:
		return codes.Ok
	}
}

// links transforms OTLP span links to span Links.
func links(links []*tracepb.Span_Link) []trace.Link {
	if len(links) == 0 {
		return nil
	}

	sl := make([]trace.Link, 0, len(links))
	for _, otLink := range links {
		// This redefinition is necessary to prevent otLink.*ID[:] copies
		// being reused -- in short we need a new otLink per iteration.
		otLink := otLink

		var tid trace.TraceID
		copy(tid[:], otLink.TraceId)
		var sid trace.SpanID
		copy(sid[:], otLink.SpanId)

		sctx := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid,
			SpanID:  sid,
		})

		sl = append(sl, trace.Link{
			SpanContext: sctx,
			Attributes:  Attributes(otLink.Attributes),
		})
	}
	return sl
}

// spanEvents transforms OTLP span events to span Events.
func spanEvents(es []*tracepb.Span_Event) []trace.Event {
	if len(es) == 0 {
		return nil
	}

	evCount := len(es)
	if evCount > maxMessageEventsPerSpan {
		evCount = maxMessageEventsPerSpan
	}
	events := make([]trace.Event, 0, evCount)
	messageEvents := 0

	// Transform message events
	for _, e := range es {
		if messageEvents >= maxMessageEventsPerSpan {
			break
		}
		messageEvents++
		events = append(events,
			trace.Event{
				Name:                  e.Name,
				Time:                  time.Unix(0, int64(e.TimeUnixNano)),
				Attributes:            Attributes(e.Attributes),
				DroppedAttributeCount: int(e.DroppedAttributesCount),
			},
		)
	}

	return events
}

// spanKind transforms a an OTLP span kind to SpanKind.
func spanKind(kind tracepb.Span_SpanKind) trace.SpanKind {
	switch kind {
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return trace.SpanKindInternal
	case tracepb.Span_SPAN_KIND_CLIENT:
		return trace.SpanKindClient
	case tracepb.Span_SPAN_KIND_SERVER:
		return trace.SpanKindServer
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return trace.SpanKindProducer
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return trace.SpanKindConsumer
	default:
		return trace.SpanKindUnspecified
	}
}
