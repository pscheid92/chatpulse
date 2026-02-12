package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	disconnectedSessionsKey = "disconnected_sessions"
	scanCount               = 100
)

func main() {
	var (
		redisURL = flag.String("redis", os.Getenv("REDIS_URL"), "Redis URL (or set REDIS_URL env)")
		dryRun   = flag.Bool("dry-run", false, "Dry run mode (don't write to Redis)")
		verbose  = flag.Bool("verbose", false, "Verbose logging")
	)
	flag.Parse()

	if *redisURL == "" {
		log.Fatal("Redis URL required (--redis or REDIS_URL env)")
	}

	// Configure logging
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(handler))

	// Connect to Redis
	opts, err := goredis.ParseURL(*redisURL)
	if err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	}
	rdb := goredis.NewClient(opts)
	defer rdb.Close()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	slog.Info("Connected to Redis", "url", sanitizeURL(*redisURL))

	// Run migration
	if err := migrateExistingSessions(ctx, rdb, *dryRun); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	slog.Info("Migration complete")
}

func migrateExistingSessions(ctx context.Context, rdb *goredis.Client, dryRun bool) error {
	start := time.Now()
	var cursor uint64
	var scanned, migrated, skipped int

	slog.Info("Starting migration", "dry_run", dryRun)

	for {
		// Scan for session keys
		keys, nextCursor, err := rdb.Scan(ctx, cursor, "session:*", scanCount).Result()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		for _, key := range keys {
			scanned++

			// Get disconnect timestamp
			lastDisconnect, err := rdb.HGet(ctx, key, "last_disconnect").Result()
			if err == goredis.Nil {
				slog.Debug("Session hash missing last_disconnect field", "key", key)
				skipped++
				continue
			}
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", key, err)
			}

			// Parse timestamp (milliseconds)
			tsMillis, err := strconv.ParseInt(lastDisconnect, 10, 64)
			if err != nil {
				slog.Warn("Invalid last_disconnect value", "key", key, "value", lastDisconnect)
				skipped++
				continue
			}

			// Skip active sessions (last_disconnect = 0)
			if tsMillis == 0 {
				slog.Debug("Skipping active session", "key", key)
				skipped++
				continue
			}

			// Extract UUID from key (session:{uuid})
			uuidStr := strings.TrimPrefix(key, "session:")

			// Convert milliseconds to seconds for sorted set score
			tsSeconds := tsMillis / 1000

			if !dryRun {
				// Add to sorted set (idempotent)
				if err := rdb.ZAdd(ctx, disconnectedSessionsKey, goredis.Z{
					Score:  float64(tsSeconds),
					Member: uuidStr,
				}).Err(); err != nil {
					return fmt.Errorf("zadd failed for %s: %w", uuidStr, err)
				}
			}

			slog.Debug("Migrated session",
				"uuid", uuidStr,
				"disconnect_time", time.Unix(tsSeconds, 0).Format(time.RFC3339))
			migrated++
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	duration := time.Since(start)
	slog.Info("Migration summary",
		"scanned", scanned,
		"migrated", migrated,
		"skipped", skipped,
		"duration_ms", duration.Milliseconds())

	// Verify sorted set size
	if !dryRun {
		count, err := rdb.ZCard(ctx, disconnectedSessionsKey).Result()
		if err != nil {
			return fmt.Errorf("zcard verification failed: %w", err)
		}
		slog.Info("Sorted set verification",
			"size", count,
			"expected", migrated)
		if count != int64(migrated) {
			slog.Warn("Sorted set size mismatch",
				"expected", migrated,
				"actual", count)
		}
	}

	return nil
}

func sanitizeURL(url string) string {
	// Hide password in Redis URL for logging
	if strings.Contains(url, "@") {
		parts := strings.Split(url, "@")
		if len(parts) == 2 {
			credParts := strings.Split(parts[0], ":")
			if len(credParts) >= 2 {
				return credParts[0] + ":***@" + parts[1]
			}
		}
	}
	return url
}
