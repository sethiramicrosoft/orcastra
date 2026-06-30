package api

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Host        string
	Port        int
	DatabaseURL string
	JWTSecret   string
	JWTIssuer   string
	JWTTTL      time.Duration

	GitHubWebhookSecret string
}

func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		Host:        getenv("HOST", "0.0.0.0"),
		Port:        getenvInt("PORT", 3000),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		JWTIssuer:   getenv("JWT_ISSUER", "orcastra"),
		JWTTTL:      getenvDuration("JWT_TTL", 24*time.Hour),

		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return Config{}, errors.New("JWT_SECRET is required")
	}
	return cfg, nil
}

func (c Config) ListenAddress() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
