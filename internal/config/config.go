package config

import (
	"os"
)

type Config struct {
	Port         string
	UpstreamURL  string
	OpenAIKey    string
	GeminiKey    string
	RedisURL     string
	DefaultMode  string
	MasterSecret string
}

func Load() *Config {
	return &Config{
		Port:         getEnv("PORT", "8080"),
		UpstreamURL:  getEnv("UPSTREAM_URL", "https://api.openai.com/v1/chat/completions"),
		OpenAIKey:    os.Getenv("OPENAI_API_KEY"),
		GeminiKey:    os.Getenv("GEMINI_API_KEY"),
		RedisURL:     os.Getenv("REDIS_URL"), // Empty fallback triggers in-memory mode
		DefaultMode:  getEnv("DEFAULT_MODE", "live"),
		MasterSecret: getEnv("TOXI_MASTER_KEY", "dev-secret-token"),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
