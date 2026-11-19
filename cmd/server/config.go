package main

import (
	"os"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// ConfigProvider loads and validates the environment. Every other provider depends on it, so a bad environment aborts startup before anything is connected or started.
//
// godotenv/autoload populates the process environment in package init, which runs before main, so os.LookupEnv already sees .env values here.
// Real environment variables win over .env, as godotenv never overrides them.
func ConfigProvider() (config.Config, error) {
	return config.Load(os.LookupEnv)
}
