package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/jackc/pgx/v5"
)

func main() {
	org := flag.String("org", "", "organization ID (required)")
	team := flag.String("team", "", "team ID (required)")
	user := flag.String("user", "", "user ID (optional, omit for service accounts)")
	name := flag.String("name", "", "human-friendly key name (required)")
	env := flag.String("env", "prod", "environment prefix")
	classification := flag.String("classification", "INTERNAL", "max classification tier: PUBLIC, INTERNAL, CONFIDENTIAL, RESTRICTED")
	expires := flag.String("expires", "365d", "expiry duration (e.g., 365d, 720h)")
	dbURL := flag.String("db-url", "", "database URL (overrides env)")
	flag.Parse()

	if *org == "" || *team == "" || *name == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\nerror: -org, -team, and -name are required")
		os.Exit(1)
	}

	// Generate key
	rawKey, err := auth.GenerateKey(*env)
	if err != nil {
		log.Fatalf("failed to generate key: %v", err)
	}

	keyHash := auth.HashKey(rawKey)
	keyPrefix := auth.KeyPrefix(rawKey)

	// Parse expiry
	dur, err := auth.ParseDuration(*expires)
	if err != nil {
		log.Fatalf("invalid expires: %v", err)
	}
	expiresAt := time.Now().Add(dur)

	// Connect to database
	dsn := *dbURL
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		host := envOrDefault("DB_HOST", "localhost")
		port := envOrDefault("DB_PORT", "5432")
		u := envOrDefault("DB_USER", "aegis")
		pass := envOrDefault("DB_PASSWORD", "aegis-dev")
		dbname := envOrDefault("DB_NAME", "aegis")
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", u, pass, host, port, dbname)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer conn.Close(ctx)

	// Serialize allowed_models as empty JSON array
	allowedModels, _ := json.Marshal([]string{})

	// Insert key
	var keyID string
	err = conn.QueryRow(ctx, `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, user_id, name, max_classification, allowed_models, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`, keyHash, keyPrefix, *org, *team, nilIfEmpty(*user), *name, *classification, allowedModels, expiresAt).Scan(&keyID)
	if err != nil {
		log.Fatalf("failed to insert key: %v", err)
	}

	fmt.Println("=== AEGIS API Key Generated ===")
	fmt.Println()
	fmt.Printf("  Key ID:         %s\n", keyID)
	fmt.Printf("  Key Prefix:     %s\n", keyPrefix)
	fmt.Printf("  Organization:   %s\n", *org)
	fmt.Printf("  Team:           %s\n", *team)
	if *user != "" {
		fmt.Printf("  User:           %s\n", *user)
	}
	fmt.Printf("  Classification: %s\n", *classification)
	fmt.Printf("  Expires:        %s\n", expiresAt.Format(time.RFC3339))
	fmt.Println()
	fmt.Println("  API Key (save this — it will NOT be shown again):")
	fmt.Printf("  %s\n", rawKey)
	fmt.Println()
	fmt.Println("================================")
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
