package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
)

func TestDocs_AreNotServedByDefault(t *testing.T) {
	router, _ := newRouter()

	for _, path := range []string{"/docs", "/openapi.json", "/openapi.yaml"} {
		assert.Equal(t, http.StatusNotFound, do(t, router, http.MethodGet, path, nil).Code, path)
	}
}

func TestDocs_AreServedWhenEnabled(t *testing.T) {
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelInfo}, "svc", &syncBuffer{})
	router := httpapi.NewRouter(httpapi.Options{Logger: logger, MaxConcurrency: 1, DocsEnabled: true})

	docs := do(t, router, http.MethodGet, "/docs", nil)
	spec := do(t, router, http.MethodGet, "/openapi.json", nil)

	assert.Equal(t, http.StatusOK, docs.Code)
	assert.Contains(t, docs.Body.String(), "swagger-ui")
	assert.Equal(t, http.StatusOK, spec.Code)
	assert.Equal(t, http.StatusOK, do(t, router, http.MethodGet, "/openapi.yaml", nil).Code)
}

func TestOpenAPI_DescribesTheEndpoints(t *testing.T) {
	doc, err := httpapi.OpenAPI("1.2.3")

	require.NoError(t, err)
	var spec struct {
		Info  struct{ Version string }
		Paths map[string]map[string]any
	}
	require.NoError(t, json.Unmarshal(doc, &spec))
	assert.Equal(t, "1.2.3", spec.Info.Version)
	assert.Contains(t, spec.Paths["/health"], "get")
	assert.Contains(t, spec.Paths["/v1/speak"], "post")
	assert.Contains(t, spec.Paths["/v1/synthesize"], "post")
}
