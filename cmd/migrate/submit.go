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
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/emitter"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runSubmit sends sealed checkpoints to a control plane.
//
// Submitting is optional. A gateway that submits nothing is a complete,
// working deployment that verifies its own chain; what it does not have is an
// outside witness to the ordering of that chain, which its own verifier cannot
// supply. See docs/adr/0006-predecessor-identity-is-not-hash-bound.md.
func runSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	endpoint := fs.String("endpoint", os.Getenv("AEGIS_CONTROL_PLANE_URL"),
		"control plane base URL (or AEGIS_CONTROL_PLANE_URL)")
	name := fs.String("name", os.Getenv("AEGIS_GATEWAY_NAME"),
		"label this gateway registers under (or AEGIS_GATEWAY_NAME); must be stable across restarts")
	batchSize := fs.Int("batch-size", 0, "stop after this many checkpoints (0 = all outstanding)")
	timeout := fs.Duration("timeout", 30*time.Second, "per-request timeout")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: aegis-migrate submit [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Submits sealed audit checkpoints to a control plane.")
		fmt.Fprintln(os.Stderr, "Sends Merkle roots, chain hashes and event ID ranges.")
		fmt.Fprintln(os.Stderr, "Sends no audit event and no request or response content.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "The bearer token is read from AEGIS_CONTROL_PLANE_TOKEN and is")
		fmt.Fprintln(os.Stderr, "never stored in the database. There is no flag for it, because a")
		fmt.Fprintln(os.Stderr, "credential passed on a command line is a credential in the process")
		fmt.Fprintln(os.Stderr, "list and in the shell history.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		log.Fatalf("submit: parse flags: %v", err)
	}

	token := os.Getenv("AEGIS_CONTROL_PLANE_TOKEN")
	if token == "" {
		log.Fatalf("submit: AEGIS_CONTROL_PLANE_TOKEN is required")
	}
	if *endpoint == "" {
		log.Fatalf("submit: -endpoint or AEGIS_CONTROL_PLANE_URL is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, resolveDSN(*dbURL))
	if err != nil {
		log.Fatalf("submit: connect to database: %v", err)
	}
	defer pool.Close()

	result, err := emitter.Run(ctx, pool, emitter.Options{
		Endpoint:    *endpoint,
		Token:       token,
		GatewayName: *name,
		BatchSize:   *batchSize,
		Timeout:     *timeout,
	})

	if result != nil {
		fmt.Printf("gateway %s: %d checkpoint(s) submitted, %d already held, cursor at checkpoint %d\n",
			result.GatewayID, result.Submitted, result.Duplicates, result.HighestSubmitted)
	}

	if err != nil {
		if errors.Is(err, emitter.ErrCoveredRangeUnknown) {
			// Naming the remedy matters here: the operator's instinct is to
			// skip the checkpoint, and skipping it turns a stuck submission
			// into a permanent hole in the evidence.
			fmt.Fprintln(os.Stderr,
				"\nThis checkpoint cannot be submitted and must not be skipped: skipping it would\n"+
					"make the next submission a gap, which the control plane records as missing\n"+
					"evidence. Submit from a gateway whose events predate no backfill, or accept a\n"+
					"declared start point with the control plane operator.")
		}
		log.Fatalf("submit: %v", err)
	}
}
