// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlpgrpc // import "go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"

	transform "github.com/moby/buildkit/util/tracing/otlptransform"
	"go.opentelemetry.io/otel/exporters/otlp"
	metricsdk "go.opentelemetry.io/otel/sdk/export/metric"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

type driver struct {
	tracesDriver tracesDriver
}

type tracesDriver struct {
	connection *connection

	lock         sync.Mutex
	tracesClient coltracepb.TraceServiceClient
}

var (
	errNoClient = errors.New("no client")
)

// NewDriver creates a new gRPC protocol driver.
func NewDriver(cc *grpc.ClientConn) otlp.ProtocolDriver {
	d := &driver{}

	d.tracesDriver = tracesDriver{
		connection: newConnection(cc, d.tracesDriver.handleNewConnection),
	}

	return d
}

func (td *tracesDriver) handleNewConnection(cc *grpc.ClientConn) {
	td.lock.Lock()
	defer td.lock.Unlock()
	if cc != nil {
		td.tracesClient = coltracepb.NewTraceServiceClient(cc)
	} else {
		td.tracesClient = nil
	}
}

// Start implements otlp.ProtocolDriver. It establishes a connection
// to the collector.
func (d *driver) Start(ctx context.Context) error {
	d.tracesDriver.connection.startConnection(ctx)
	return nil
}

// Stop implements otlp.ProtocolDriver. It shuts down the connection
// to the collector.
func (d *driver) Stop(ctx context.Context) error {
	return d.tracesDriver.connection.shutdown(ctx)
}

func (d *driver) ExportMetrics(ctx context.Context, cps metricsdk.CheckpointSet, selector metricsdk.ExportKindSelector) error {
	return errors.New("metrics in not implemented")
}

// ExportTraces implements otlp.ProtocolDriver. It transforms spans to
// protobuf binary format and sends the result to the collector.
func (d *driver) ExportTraces(ctx context.Context, ss []*tracesdk.SpanSnapshot) error {
	if !d.tracesDriver.connection.connected() {
		return fmt.Errorf("traces exporter is disconnected: %w", d.tracesDriver.connection.lastConnectError())
	}
	ctx, cancel := d.tracesDriver.connection.contextWithStop(ctx)
	defer cancel()
	ctx, tCancel := context.WithTimeout(ctx, 30*time.Second)
	defer tCancel()

	protoSpans := transform.SpanData(ss)
	if len(protoSpans) == 0 {
		return nil
	}

	return d.tracesDriver.uploadTraces(ctx, protoSpans)
}

func (td *tracesDriver) uploadTraces(ctx context.Context, protoSpans []*tracepb.ResourceSpans) error {
	ctx = td.connection.contextWithMetadata(ctx)
	err := func() error {
		td.lock.Lock()
		defer td.lock.Unlock()
		if td.tracesClient == nil {
			return errNoClient
		}
		_, err := td.tracesClient.Export(ctx, &coltracepb.ExportTraceServiceRequest{
			ResourceSpans: protoSpans,
		})
		return err
	}()
	if err != nil {
		td.connection.setStateDisconnected(err)
	}
	return err
}
