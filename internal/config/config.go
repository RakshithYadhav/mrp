package config

import "os"

type Config struct {
	DatabaseURL string
	Port string
}

func Load() Config {
	return Config{
		DatabaseURL: getenv("DATABASE_URL", "postgres://mrp:mrp@localhost:5433/mrp"),
		Port: getenv("PORT", "8090"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}