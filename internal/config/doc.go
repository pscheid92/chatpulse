// Package config provides environment-based configuration.
//
// Loads from .env file (godotenv), maps to Config struct via go-simpler/env struct tags.
// Validates required fields and encryption key format.
package config
