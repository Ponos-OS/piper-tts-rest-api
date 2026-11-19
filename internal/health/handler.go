// Package health serves GET /health.
package health

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Path is not logged or traced: it is polled by health checks.
const Path = "/health"

// Handler serves /health.
type Handler struct{}

type output struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// RegisterRoutes mounts the endpoint on api.
func (Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        Path,
		Summary:     "Health check",
		Tags:        []string{"Health"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "The service is up.",
				Content:     map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: "string", Examples: []any{"OK"}}}},
			},
		},
	}, func(_ context.Context, _ *struct{}) (*output, error) {
		return &output{ContentType: "text/plain; charset=utf-8", Body: []byte("OK")}, nil
	})
}
