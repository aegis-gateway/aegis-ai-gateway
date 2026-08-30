// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
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
	allowedModels := flag.String("allowed-models", "",
		"comma-separated model aliases this key may use (required), or 'any' for no restriction")
	modelsConfig := flag.String("models-config", defaultModelsConfig(),
		"models.yaml to validate -allowed-models against; unreadable means the check is skipped")
	flag.Parse()

	if *org == "" || *team == "" || *name == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\nerror: -org, -team, and -name are required")
		os.Exit(1)
	}
	if *allowedModels == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\nerror: -allowed-models is required.\n"+
			"An empty allowlist does not mean 'no access', it means NO RESTRICTION: the\n"+
			"gateway permits every configured model when the list is empty. Issuing keys\n"+
			"without saying so is how every key ends up unrestricted. Name the models, or\n"+
			"pass -allowed-models=any to grant everything deliberately.")
		os.Exit(1)
	}

	models, unrestricted, err := parseAllowedModels(*allowedModels, *modelsConfig)
	if err != nil {
		log.Fatalf("-allowed-models: %v", err)
	}

	// Require AEGIS_KEY_PEPPER for HMAC-SHA256 hashing of new keys
	pepper := os.Getenv("AEGIS_KEY_PEPPER")
	if pepper == "" {
		log.Fatal("AEGIS_KEY_PEPPER is not set; export a secret pepper (min 32 chars) before issuing keys")
	}
	if len(pepper) < 32 {
		log.Fatal("AEGIS_KEY_PEPPER is too short; use at least 32 characters")
	}

	// Generate key
	rawKey, err := auth.GenerateKey(*env)
	if err != nil {
		log.Fatalf("failed to generate key: %v", err)
	}

	keyHash := auth.HashKeyV2(rawKey, pepper)
	hashVersion := 2
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
	defer func() { _ = conn.Close(ctx) }()

	// An empty array is the unrestricted value, which is why -allowed-models=any
	// has to be asked for rather than defaulted to.
	allowedModelsJSON, err := json.Marshal(models)
	if err != nil {
		log.Fatalf("encoding allowed_models: %v", err)
	}

	// Insert key
	var keyID string
	err = conn.QueryRow(ctx, `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, user_id, name, max_classification, allowed_models, expires_at, hash_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, keyHash, keyPrefix, *org, *team, nilIfEmpty(*user), *name, *classification, allowedModelsJSON, expiresAt, hashVersion).Scan(&keyID)
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
	if unrestricted {
		fmt.Printf("  Allowed models: ANY (unrestricted)\n")
	} else {
		fmt.Printf("  Allowed models: %s\n", strings.Join(models, ", "))
	}
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

// parseAllowedModels turns the -allowed-models value into the JSON array the
// api_keys column holds, and reports whether the key is unrestricted.
//
// "any" yields an empty slice, which is the value the gateway reads as no
// restriction: modelAllowed returns true when the list is empty. That is the
// whole reason this flag is required rather than defaulted. Before it existed,
// keygen wrote an empty array unconditionally, so every key it issued could use
// every configured model and nothing said so at issue time.
//
// Names are validated against models.yaml when it can be read. A misspelled
// alias is otherwise a key that authenticates and is refused every model,
// because matching is exact string equality against the alias, and the operator
// finds out from a caller rather than from the tool.
func parseAllowedModels(raw, modelsPath string) ([]string, bool, error) {
	if strings.EqualFold(strings.TrimSpace(raw), "any") {
		return []string{}, true, nil
	}

	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		alias := strings.TrimSpace(part)
		if alias == "" {
			return nil, false, fmt.Errorf(
				"empty entry in %q; list aliases separated by commas, or pass 'any'", raw)
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		out = append(out, alias)
	}

	// Migration 015 constrains the column to a JSON array of strings and the
	// reader fails closed on anything else, so a malformed value here would
	// produce a key that cannot authenticate at all.
	var cfg config.ModelsConfig
	if err := config.LoadFile(modelsPath, &cfg); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: could not read %s (%v); -allowed-models was not checked against the "+
				"configured aliases, so a typo here becomes a key that is refused every model\n",
			modelsPath, err)
		return out, false, nil
	}
	var unknown []string
	for _, alias := range out {
		if _, ok := cfg.Models[alias]; !ok {
			unknown = append(unknown, alias)
		}
	}
	if len(unknown) > 0 {
		known := make([]string, 0, len(cfg.Models))
		for alias := range cfg.Models {
			known = append(known, alias)
		}
		sort.Strings(known)
		return nil, false, fmt.Errorf(
			"%s is not configured in %s. Matching is exact, so this key would be refused "+
				"that model on every request. Configured aliases: %s",
			strings.Join(unknown, ", "), modelsPath, strings.Join(known, ", "))
	}
	return out, false, nil
}

// defaultModelsConfig locates models.yaml the way the running image does.
//
// The Dockerfile installs configs at /etc/aegis/configs and exports
// AEGIS_CONFIG_DIR, and the entrypoint execs keygen without changing directory,
// so a repo-relative default is never readable there. That would silently skip
// alias validation on exactly the documented Docker path, which is the one an
// operator issuing a first key is most likely to follow.
func defaultModelsConfig() string {
	if dir := os.Getenv("AEGIS_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "models.yaml")
	}
	return filepath.Join("configs", "models.yaml")
}
