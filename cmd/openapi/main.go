// Command openapi prints the OpenAPI document of the service.
package main

import (
	"fmt"
	"os"

	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
)

func main() {
	version, _ := os.LookupEnv("SERVICE_VERSION")
	if version == "" {
		version = "unknown"
	}

	doc, err := httpapi.OpenAPI(version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
	os.Stdout.Write(doc) //nolint:errcheck,gosec // nothing to do when stdout is closed.
}
