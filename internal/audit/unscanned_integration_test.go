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

package audit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/audittest"
)

// The unscanned-part columns as an assessor reads them: from the stored row,
// through both forwarding paths, after the logger's normalization.
//
// Checked against the database rather than the constructed Event because the
// failure that matters here is a lost row. A negative count or an over-long
// type list would fail a CHECK or a varchar bound, and the asynchronous write
// would drop the whole event with only a log line.
func TestLogger_WritesUnscannedPartColumns(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// Before Serialise and never deferred; see TestLogModelDenied_WritesItsOwnEventType.
	t.Cleanup(pool.Close)
	audittest.Serialise(t, pool)

	i64 := func(v int64) *int64 { return &v }
	stamp := time.Now().UTC().Format("20060102150405.000000")
	base := audit.CompletedRequest{
		OrganizationID: "org-u",
		TeamID:         "team-u",
		KeyID:          "3f2a1c44-9b7e-4d21-8a55-0c9f7b1e4d33",
		KeyPrefix:      "aegis-prod-testpfx",
		Endpoint:       "/v1/chat/completions",
		RequestedModel: "aegis-fast",
		ProviderKey:    "openai",
		StatusCode:     200,
		IPAddress:      "203.0.113.1:1234",
	}

	type want struct {
		parts, size *int64
		types       *string
	}
	str := func(s string) *string { return &s }
	cases := []struct {
		name    string
		failure bool
		parts   *int64
		size    *int64
		types   string
		want    want
	}{
		{name: "complete", parts: i64(2), size: i64(48213), types: "image/png,image/jpeg",
			want: want{i64(2), i64(48213), str("image/jpeg,image/png")}},
		{name: "failure", failure: true, parts: i64(1), size: i64(1024), types: "image/webp",
			want: want{i64(1), i64(1024), str("image/webp")}},
		{name: "none", want: want{}},
		// Would trip the CHECK and cost the row if written as given.
		{name: "negative", parts: i64(-1), size: i64(-1), types: "image/png", want: want{}},
		// Would trip VARCHAR(256) if written as given, and is caller text.
		{name: "hostile", parts: i64(1), size: i64(10),
			types: "image/png," + string(make([]byte, 400)) + "CANARY_PAYLOAD_8f4a2b91e3c7d056",
			want:  want{i64(1), i64(10), str(audit.UnlistedMediaType + ",image/png")}},
	}

	logger := audit.NewLogger(pool)
	ids := map[string]string{}
	for _, tc := range cases {
		req := base
		req.RequestID = "req_un_" + tc.name + "_" + stamp
		req.UnscannedParts, req.UnscannedBytes, req.UnscannedPartTypes = tc.parts, tc.size, tc.types
		ids[tc.name] = req.RequestID
		if tc.failure {
			logger.LogProviderFailure(req, audit.ReasonProviderError)
		} else {
			logger.LogRequestComplete(req)
		}
	}
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !logger.Drain(drainCtx) {
		t.Fatal("the audit writes did not drain before the deadline")
	}

	for _, tc := range cases {
		var got want
		err := pool.QueryRow(ctx, `
			SELECT unscanned_parts, unscanned_bytes, unscanned_part_types
			FROM audit_events WHERE request_id = $1`, ids[tc.name]).
			Scan(&got.parts, &got.size, &got.types)
		if err != nil {
			t.Errorf("%s: reading the row back: %v (a lost row is the failure this guards)", tc.name, err)
			continue
		}
		if !eqI64(got.parts, tc.want.parts) || !eqI64(got.size, tc.want.size) || !eqStr(got.types, tc.want.types) {
			t.Errorf("%s: stored (%v, %v, %v), want (%v, %v, %v)", tc.name,
				show(got.parts), show(got.size), showS(got.types),
				show(tc.want.parts), show(tc.want.size), showS(tc.want.types))
		}
	}

	for _, id := range ids {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE request_id = $1`, id)
	}
}

func eqI64(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqStr(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func show(p *int64) any {
	if p == nil {
		return "NULL"
	}
	return *p
}

func showS(p *string) any {
	if p == nil {
		return "NULL"
	}
	return *p
}
