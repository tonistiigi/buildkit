package detect

import (
	"context"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"go.opentelemetry.io/otel/trace"
)

type ExporterDetector func() (sdktrace.SpanExporter, error)

type detector struct {
	f        ExporterDetector
	priority int
}

var detectors map[string]detector
var once sync.Once
var tracer trace.Tracer
var exporter sdktrace.SpanExporter
var closers []func(context.Context) error
var err error

func Register(name string, exp ExporterDetector, priority int) {
	if detectors == nil {
		detectors = map[string]detector{}
	}
	detectors[name] = detector{
		f:        exp,
		priority: priority,
	}
}

func detectExporter() (sdktrace.SpanExporter, error) {
	if n := os.Getenv("OTEL_TRACES_EXPORTER"); n != "" {
		d, ok := detectors[n]
		if !ok {
			if n == "none" {
				return nil, nil
			}
			return nil, errors.Errorf("unsupported opentelemetry tracer %v", n)
		}
		return d.f()
	}
	arr := make([]detector, 0, len(detectors))
	for _, d := range detectors {
		arr = append(arr, d)
	}
	sort.Slice(arr, func(i, j int) bool {
		return arr[i].priority < arr[j].priority
	})
	for _, d := range arr {
		exp, err := d.f()
		if err != nil {
			return nil, err
		}
		if exp != nil {
			return exp, nil
		}
	}
	return nil, nil
}

func detect() error {
	tp := trace.NewNoopTracerProvider()
	tracer = tp.Tracer("")

	exp, err := detectExporter()
	if err != nil {
		return err
	}

	if exp == nil {
		return nil
	}

	res, err := resource.Detect(context.Background(), serviceNameDetector{}, resource.FromEnv{}, resource.Host{}, resource.TelemetrySDK{})
	if err != nil {
		return err
	}

	sp := sdktrace.NewBatchSpanProcessor(exp)

	sdktp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sp), sdktrace.WithResource(res))
	closers = append(closers, sdktp.Shutdown)
	tp = sdktp
	tracer = tp.Tracer("")
	exporter = exp

	return nil
}

func Tracer() (trace.Tracer, error) {
	once.Do(func() {
		if err1 := detect(); err1 != nil {
			err = err1
		}
	})
	b, _ := strconv.ParseBool(os.Getenv("OTEL_INGORE_ERROR"))
	if err != nil && !b {
		return nil, err
	}
	return tracer, nil
}

func Exporter() (sdktrace.SpanExporter, error) {
	_, err := Tracer()
	if err != nil {
		return nil, err
	}
	return exporter, nil
}

func Shutdown(ctx context.Context) error {
	for _, c := range closers {
		if err := c(ctx); err != nil {
			return err
		}
	}
	return nil
}

type serviceNameDetector struct{}

func (serviceNameDetector) Detect(ctx context.Context) (*resource.Resource, error) {
	return resource.StringDetector(
		semconv.ServiceNameKey,
		func() (string, error) {
			if n := os.Getenv("OTEL_SERVICE_NAME"); n != "" {
				return n, nil
			}
			return os.Args[0], nil
		},
	).Detect(ctx)
}
