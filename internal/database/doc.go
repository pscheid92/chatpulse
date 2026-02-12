// Package database provides PostgreSQL connectivity and repositories.
//
// Uses pgx for connection pooling, tern for migrations, and sqlc for type-safe query generation.
// Repositories implement domain interfaces: UserRepository, ConfigRepository, EventSubRepository.
package database
