package transform

import (
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"go.opentelemetry.io/otel/sdk/resource"
)

// Resource transforms a Resource into an OTLP Resource.
func Resource(r *resourcepb.Resource) *resource.Resource {
	if r == nil {
		return nil
	}

	return resource.NewWithAttributes(Attributes(r.Attributes)...)
}
