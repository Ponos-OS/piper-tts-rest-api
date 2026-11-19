// Command server runs the Piper TTS REST API.
package main

import (
	"net/http"
	"time"

	"go.uber.org/fx"

	_ "github.com/joho/godotenv/autoload"
)

func main() {
	fx.New(
		fx.Provide(
			ConfigProvider,
			TelemetryProvider,
			LoggerProvider,
			PiperProvider,
			BrokerProvider,
			NewHTTPRouter,
			NewHTTPServer,
		),
		fx.Invoke(
			StartWorker,
			func(*http.Server) {},
		),
		fx.StopTimeout(1*time.Minute),
	).Run()
}
