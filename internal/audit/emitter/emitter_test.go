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

package emitter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/audittest"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/emitter"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testGatewayID = "b2c64e2a-5d2b-44af-9c76-b09e34168a6e"

// fakeControlPlane is a minimal server implementing the protocol's chain rules,
// enough to exercise the emitter without the real service.
type fakeControlPlane struct {
	mu sync.Mutex

	// received is every checkpoint accepted, in order.
	received []controlplanev1.CheckpointSubmission
	// statusReports is every sealing status the gateway reported.
	statusReports []controlplanev1.GatewayStatusReport
	// registrations counts how many times the gateway registered.
	registrations int
	// rejectAt, when non-zero, makes the server reject that checkpoint ID with
	// a chain gap, standing in for a control plane that holds different state.
	rejectAt int64
	// failNext, when true, makes the next submission fail at the transport
	// level, standing in for a dropped connection mid-run.
	failNext bool
	// hollowAckAt, when non-zero, makes the server answer that checkpoint with
	// a 2xx whose body decodes but identifies nothing, standing in for a proxy
	// or misconfigured endpoint that returns success without storing anything.
	hollowAckAt int64

	server *httptest.Server
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gateways", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		f.mu.Lock()
		f.registrations++
		f.mu.Unlock()

		var req controlplanev1.GatewayRegistration
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, controlplanev1.GatewayRegistrationResponse{
			GatewayID:    testGatewayID,
			OrgID:        "11111111-1111-4111-8111-111111111111",
			Name:         req.Name,
			RegisteredAt: controlplanev1.NewTimestamp(time.Now()),
		})
	})

	mux.HandleFunc("POST /v1/gateways/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		var report controlplanev1.GatewayStatusReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := report.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, controlplanev1.Error{
				Code: controlplanev1.ErrCodeInvalidRequest, Message: err.Error(),
			})
			return
		}
		f.mu.Lock()
		f.statusReports = append(f.statusReports, report)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, controlplanev1.GatewayStatusResponse{
			GatewayID:  report.GatewayID,
			ReceivedAt: controlplanev1.NewTimestamp(time.Now()),
			SealState:  report.SealState,
		})
	})

	mux.HandleFunc("POST /v1/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		var sub controlplanev1.CheckpointSubmission
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		if f.failNext {
			f.failNext = false
			// Close the connection without a response, which is what a dropped
			// connection looks like to the client.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					conn.Close() //nolint:errcheck
					return
				}
			}
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}

		if f.hollowAckAt != 0 && sub.CheckpointID == f.hollowAckAt {
			// A 2xx that decodes into the response type and names nothing.
			writeJSON(w, http.StatusCreated, controlplanev1.CheckpointSubmissionResponse{})
			return
		}

		if f.rejectAt != 0 && sub.CheckpointID == f.rejectAt {
			writeJSON(w, http.StatusUnprocessableEntity, controlplanev1.Error{
				Code:    controlplanev1.ErrCodeChainGap,
				Message: "the stored chain ends at checkpoint 1, so checkpoint 2 was expected",
			})
			return
		}

		// Idempotent on an identical resubmission, as the real service is.
		for _, prior := range f.received {
			if prior.CheckpointID == sub.CheckpointID {
				if prior.CheckpointHash != sub.CheckpointHash {
					writeJSON(w, http.StatusConflict, controlplanev1.Error{
						Code:    controlplanev1.ErrCodeChainFork,
						Message: "a different checkpoint is already stored at this sequence",
					})
					return
				}
				writeJSON(w, http.StatusOK, controlplanev1.CheckpointSubmissionResponse{
					GatewayID: sub.GatewayID, CheckpointID: sub.CheckpointID,
					ReceivedAt: controlplanev1.NewTimestamp(time.Now()), Duplicate: true,
					ChainStatus: controlplanev1.ChainStatusLinked,
				})
				return
			}
		}

		f.received = append(f.received, sub)
		writeJSON(w, http.StatusCreated, controlplanev1.CheckpointSubmissionResponse{
			GatewayID: sub.GatewayID, CheckpointID: sub.CheckpointID,
			ReceivedAt:  controlplanev1.NewTimestamp(time.Now()),
			ChainStatus: controlplanev1.ChainStatusLinked,
		})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

const testToken = "aegis-cp-test-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (f *fakeControlPlane) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		writeJSON(w, http.StatusUnauthorized, controlplanev1.Error{
			Code: controlplanev1.ErrCodeUnauthorized, Message: "a valid bearer token is required",
		})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeControlPlane) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// --- database helpers -------------------------------------------------------

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping the tests that need Postgres")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// internal/audit/checkpoint truncates and seals the same tables, and
	// package binaries run in parallel against one database.
	audittest.Serialise(t, pool)
	return pool
}

// sealFixture writes n events and seals them into checkpoints of the given size.
func sealFixture(t *testing.T, db *pgxpool.Pool, events, batchSize int) {
	t.Helper()
	ctx := context.Background()

	if _, err := db.Exec(ctx,
		"TRUNCATE audit_events, audit_checkpoints, control_plane_state RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("resetting the audit tables: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := range events {
		if _, err := db.Exec(ctx, `
			INSERT INTO audit_events (request_id, timestamp, event_type)
			VALUES ($1, $2, 'test_event')
		`, "req-emitter-test", base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("inserting an audit event: %v", err)
		}
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{
		LagSeconds: checkpoint.SealLag(0), BatchSize: batchSize,
	}); err != nil {
		t.Fatalf("sealing: %v", err)
	}
}

func runEmitter(t *testing.T, db *pgxpool.Pool, f *fakeControlPlane, batchSize int) (*emitter.Result, error) {
	t.Helper()
	return emitter.Run(context.Background(), db, emitter.Options{
		Endpoint:    f.server.URL,
		Token:       testToken,
		GatewayName: "emitter-test",
		BatchSize:   batchSize,
		Timeout:     10 * time.Second,
	})
}

// --- tests ------------------------------------------------------------------

// TestEmitterSubmitsTheWholeChain is the end-to-end case: seal real events,
// submit them, and confirm the control plane received a chain it can verify.
func TestEmitterSubmitsTheWholeChain(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 20, 4) // five checkpoints

	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if result.Submitted != 5 {
		t.Errorf("submitted %d checkpoints, want 5", result.Submitted)
	}
	if f.count() != 5 {
		t.Fatalf("the control plane received %d checkpoints, want 5", f.count())
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for i, sub := range f.received {
		// The chain must arrive in order and joined up.
		if want := int64(i + 1); sub.CheckpointID != want {
			t.Errorf("position %d holds checkpoint %d, want %d", i, sub.CheckpointID, want)
		}
		if i == 0 {
			if sub.PrevCheckpointID != nil {
				t.Errorf("the first checkpoint claims predecessor %d", *sub.PrevCheckpointID)
			}
			if sub.PrevCheckpointHash != controlplanev1.GenesisPrevHash {
				t.Errorf("the first checkpoint does not carry the genesis constant")
			}
		} else if sub.PrevCheckpointHash != f.received[i-1].CheckpointHash {
			t.Errorf("checkpoint %d binds %s but checkpoint %d hashes to %s",
				sub.CheckpointID, sub.PrevCheckpointHash,
				f.received[i-1].CheckpointID, f.received[i-1].CheckpointHash)
		}

		// Every checkpoint must be re-derivable from what was transmitted.
		// This is the property that makes the submission evidence rather than
		// a report, and it is checked here against the bytes that went over
		// the wire rather than against the database row.
		if err := controlplanev1.VerifyCheckpointHash(&f.received[i]); err != nil {
			t.Errorf("a transmitted checkpoint cannot be re-derived: %v", err)
		}
		if sub.CoveredFrom.IsZero() || sub.CoveredTo.IsZero() {
			t.Errorf("checkpoint %d was transmitted without a covered time range", sub.CheckpointID)
		}
		if sub.CoveredTo.Before(sub.CoveredFrom.Time) {
			t.Errorf("checkpoint %d has a covered range running backwards", sub.CheckpointID)
		}
	}
}

// TestEmitterCarriesNoPayload is the zero-retention check at the wire.
//
// The schema guard covers what the control plane stores. This covers what the
// gateway transmits, which is the earlier and more important boundary: content
// that never leaves cannot be stored anywhere.
func TestEmitterCarriesNoPayload(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)

	ctx := context.Background()
	if _, err := db.Exec(ctx,
		"TRUNCATE audit_events, audit_checkpoints, control_plane_state RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	// An event carrying distinctive strings in every column that could hold
	// them, including the promoted detail columns the leaf hash covers.
	// Short enough for request_id, which is VARCHAR(50). The point is that the
	// string appears in every column of the event that could hold content,
	// including the two widest columns migration 013 promoted out of the old
	// metadata JSONB. Those are where a future mistake would most plausibly put
	// caller text, so they are the ones worth planting a canary in.
	const canary = "CANARY-NEVER-LEAVES"
	if _, err := db.Exec(ctx, `
		INSERT INTO audit_events (request_id, timestamp, event_type, organization_id,
		                          user_agent, endpoint, error_message, reason, error_detail)
		VALUES ($1, NOW() - INTERVAL '1 hour', 'test_event', $2, $3, $4, $5, $6, $7)
	`, "req-"+canary, "org-"+canary, "ua-"+canary, "/v1/"+canary, "err-"+canary,
		"reason-"+canary, "detail-"+canary); err != nil {
		t.Fatalf("inserting the canary event: %v", err)
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if _, err := runEmitter(t, db, f, 0); err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if f.count() == 0 {
		t.Fatal("nothing was submitted, so this test verified nothing")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.received {
		encoded, err := json.Marshal(f.received[i])
		if err != nil {
			t.Fatalf("re-encoding a submission: %v", err)
		}
		if strings.Contains(string(encoded), canary) {
			t.Errorf("a submitted checkpoint carries content from the audit event:\n%s", encoded)
		}
		// The Merkle root covers the event, which is the point, but it must
		// not reveal it.
		if strings.Contains(string(f.received[i].MerkleRoot), canary) {
			t.Error("the Merkle root contains event content")
		}
	}
}

// TestEmitterIsIdempotent covers a rerun. A scheduled emitter runs whether or
// not anything new was sealed, and a rerun must not resubmit or double count.
func TestEmitterIsIdempotent(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 12, 4) // three checkpoints

	if _, err := runEmitter(t, db, f, 0); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if second.Submitted != 0 || second.Duplicates != 0 {
		t.Errorf("a rerun with nothing new sent %d checkpoints and %d duplicates, want none",
			second.Submitted, second.Duplicates)
	}
	if f.count() != 3 {
		t.Errorf("the control plane holds %d checkpoints after two runs, want 3", f.count())
	}
	if f.registrations != 1 {
		t.Errorf("the gateway registered %d times, want 1; a second registration means the "+
			"stored identity was not reused", f.registrations)
	}
}

// TestEmitterResumesAfterAnInterruption covers a run that dies mid-chain. The
// cursor advances per checkpoint, so the next run continues rather than
// replaying the batch.
func TestEmitterResumesAfterAnInterruption(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 20, 4) // five checkpoints

	// Fail on the third submission.
	f.mu.Lock()
	f.failNext = false
	f.mu.Unlock()

	if _, err := runEmitter(t, db, f, 2); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if f.count() != 2 {
		t.Fatalf("the batch limit was not honoured: %d checkpoints received, want 2", f.count())
	}

	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}
	if result.Submitted != 3 {
		t.Errorf("the resumed run submitted %d checkpoints, want the remaining 3", result.Submitted)
	}
	if result.Duplicates != 0 {
		t.Errorf("the resumed run replayed %d checkpoints the control plane already held",
			result.Duplicates)
	}
	if f.count() != 5 {
		t.Errorf("the control plane holds %d checkpoints, want 5", f.count())
	}
}

// TestEmitterStopsOnAChainDiscontinuity covers the disagreement case. Retrying
// cannot resolve it, so the run must stop and say so rather than continuing
// past the checkpoint the control plane refused.
func TestEmitterStopsOnAChainDiscontinuity(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 20, 4)

	f.mu.Lock()
	f.rejectAt = 2
	f.mu.Unlock()

	_, err := runEmitter(t, db, f, 0)
	if err == nil {
		t.Fatal("a refused checkpoint did not stop the run")
	}
	if !strings.Contains(err.Error(), "will not resolve by retrying") {
		t.Errorf("the error does not distinguish a disagreement from a transport failure: %v", err)
	}
	if f.count() != 1 {
		t.Errorf("the run continued past the refusal: %d checkpoints received, want 1", f.count())
	}
}

// TestEmitterRefusesADifferentControlPlane covers a cursor built against one
// deployment being pointed at another. The identity was issued by the first,
// and the second never saw the checkpoints the cursor claims were accepted.
func TestEmitterRefusesADifferentControlPlane(t *testing.T) {
	db := testDB(t)
	first := newFakeControlPlane(t)
	sealFixture(t, db, 8, 4)

	if _, err := runEmitter(t, db, first, 0); err != nil {
		t.Fatalf("first control plane: %v", err)
	}

	second := newFakeControlPlane(t)
	_, err := runEmitter(t, db, second, 0)
	if err == nil {
		t.Fatal("the emitter replayed a cursor against a different control plane")
	}
	if !strings.Contains(err.Error(), "is not valid there") {
		t.Errorf("the error does not explain why the stored identity cannot be reused: %v", err)
	}
	if second.count() != 0 {
		t.Errorf("the second control plane received %d checkpoints", second.count())
	}
}

// TestEmitterRejectsABadToken confirms the credential is actually required.
func TestEmitterRejectsABadToken(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 4, 4)

	_, err := emitter.Run(context.Background(), db, emitter.Options{
		Endpoint:    f.server.URL,
		Token:       "aegis-cp-test-wrongwrongwrongwrongwrongwrongwr",
		GatewayName: "emitter-test",
		Timeout:     10 * time.Second,
	})
	if err == nil {
		t.Fatal("a wrong token was accepted")
	}
	if f.count() != 0 {
		t.Errorf("checkpoints were submitted with a wrong token")
	}
}

// TestEmitterReportsSealingStatus covers the signal that separates a gateway
// which has deliberately stopped sealing from one that has fallen off the
// network. Both stop submitting checkpoints; only the first can say why.
func TestEmitterReportsSealingStatus(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 8, 4)

	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if result.SealState != controlplanev1.SealStateAdvancing {
		t.Errorf("seal state is %q, want %q after a complete seal",
			result.SealState, controlplanev1.SealStateAdvancing)
	}

	f.mu.Lock()
	reports := append([]controlplanev1.GatewayStatusReport(nil), f.statusReports...)
	f.mu.Unlock()

	if len(reports) != 1 {
		t.Fatalf("%d status reports received, want 1", len(reports))
	}
	report := reports[0]
	if report.SealState != controlplanev1.SealStateAdvancing {
		t.Errorf("reported seal state %q, want %q", report.SealState, controlplanev1.SealStateAdvancing)
	}
	if report.UnsealedEventCount != 0 {
		t.Errorf("reported %d unsealed events, want 0", report.UnsealedEventCount)
	}
	if report.LastCheckpointID == nil || *report.LastCheckpointID != 2 {
		t.Errorf("reported last checkpoint %v, want 2", report.LastCheckpointID)
	}
}

// TestEmitterReportsAGapPause is the case the signal exists for.
//
// A gap in audit event IDs stops the sealer permanently, so the gateway submits
// nothing further. Without a status report that is indistinguishable from a
// gateway that stopped running.
func TestEmitterReportsAGapPause(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	ctx := context.Background()

	sealFixture(t, db, 4, 4) // one checkpoint covering events 1-4

	// Insert two more events and delete the first of them, leaving a hole
	// immediately after the sealed range. This is what a rolled-back insert
	// leaves behind, and the sealer refuses to seal past it.
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 2 {
		if _, err := db.Exec(ctx, `
			INSERT INTO audit_events (request_id, timestamp, event_type)
			VALUES ($1, $2, 'test_event')
		`, "req-gap", base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("inserting an audit event: %v", err)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM audit_events WHERE id = 5`); err != nil {
		t.Fatalf("creating the gap: %v", err)
	}

	// The events beyond the gap were written an hour ago by sealFixture, so the
	// gap is older than the lag window and the sealer has already stopped at
	// it. A gap younger than the window is a different state; see
	// TestEmitterReportsAGapStillInsideTheLagWindow.
	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if result.SealState != controlplanev1.SealStatePausedAtGap {
		t.Fatalf("seal state is %q, want %q", result.SealState, controlplanev1.SealStatePausedAtGap)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statusReports) != 1 {
		t.Fatalf("%d status reports received, want 1", len(f.statusReports))
	}
	report := f.statusReports[0]

	// The two numbers an operator needs in order to go and look.
	if report.LastSealedEventID != 4 {
		t.Errorf("last_sealed_event_id is %d, want 4", report.LastSealedEventID)
	}
	if report.FirstUnsealedEventID == nil || *report.FirstUnsealedEventID != 6 {
		t.Errorf("first_unsealed_event_id is %v, want 6", report.FirstUnsealedEventID)
	}
	// Age is reported independently of the threshold, so a consumer can judge
	// the gap without agreeing with the gateway's declared window.
	if report.GapAgeSeconds == nil || *report.GapAgeSeconds <= 0 {
		t.Errorf("gap_age_seconds is %v, want a positive age", report.GapAgeSeconds)
	}
	if report.SealLagSeconds != controlplanev1.DefaultSealLagSeconds {
		t.Errorf("seal_lag_seconds is %d, want the declared default %d",
			report.SealLagSeconds, controlplanev1.DefaultSealLagSeconds)
	}
	// The count bounds what is unattested. A pause with one unsealed event and
	// one with a million are different situations wearing the same label.
	if report.UnsealedEventCount != 1 {
		t.Errorf("reported %d unsealed events, want 1", report.UnsealedEventCount)
	}
}

// TestEmitterReportsNeverSealed covers a gateway holding events and no
// checkpoint, whose chain attests nothing yet.
func TestEmitterReportsNeverSealed(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	ctx := context.Background()

	if _, err := db.Exec(ctx,
		"TRUNCATE audit_events, audit_checkpoints, control_plane_state RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("resetting: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit_events (request_id, timestamp, event_type)
		VALUES ('req-unsealed', NOW() - INTERVAL '1 hour', 'test_event')
	`); err != nil {
		t.Fatalf("inserting an audit event: %v", err)
	}

	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if result.SealState != controlplanev1.SealStateNeverSealed {
		t.Errorf("seal state is %q, want %q", result.SealState, controlplanev1.SealStateNeverSealed)
	}
	if result.Submitted != 0 {
		t.Errorf("submitted %d checkpoints from a gateway that has sealed none", result.Submitted)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statusReports) != 1 {
		t.Fatalf("%d status reports received, want 1; a gateway with nothing to submit is "+
			"exactly the one whose status matters", len(f.statusReports))
	}
	if f.statusReports[0].LastCheckpointID != nil {
		t.Errorf("reported a last checkpoint of %v on a gateway that has sealed none",
			f.statusReports[0].LastCheckpointID)
	}
}

// TestEmitterStopsOnAnUnidentifiedAcknowledgement covers a 2xx that decodes
// but acknowledges nothing.
//
// The cursor is what makes submission resumable, and it advances on the
// strength of the response. If an empty body counts as success the cursor
// moves past a checkpoint the control plane never stored, and because the next
// run resumes after the cursor that checkpoint is never offered again. The
// result is a permanent hole in the evidence produced by the mechanism that
// exists to prevent holes, so the run has to stop instead.
func TestEmitterStopsOnAnUnidentifiedAcknowledgement(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	sealFixture(t, db, 20, 4) // five checkpoints

	f.mu.Lock()
	f.hollowAckAt = 2
	f.mu.Unlock()

	_, err := runEmitter(t, db, f, 0)
	if err == nil {
		t.Fatal("an acknowledgement naming no checkpoint was accepted as success")
	}
	if !strings.Contains(err.Error(), "acknowledged checkpoint 0") {
		t.Errorf("the error does not say the acknowledgement identified nothing: %v", err)
	}

	// The cursor must still sit at checkpoint 1: the run stopped at 2 rather
	// than stepping over it.
	var cursor int64
	if err := db.QueryRow(context.Background(),
		"SELECT last_submitted_checkpoint FROM control_plane_state WHERE singleton").
		Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 1 {
		t.Errorf("the cursor advanced to %d past an unstored checkpoint; want 1", cursor)
	}
}

// TestEmitterReportsAGapStillInsideTheLagWindow is the false positive this
// state exists to remove.
//
// BIGSERIAL hands out an ID at insert and the row becomes visible at commit, so
// a transaction in flight leaves a gap that resolves itself moments later. The
// sealer's lag window exists to let exactly that happen: it will not consider
// events younger than the window, so it has not attempted to seal past this gap
// and nothing is stuck.
//
// Reporting that as paused would be wrong, and worse than wrong: an operator
// who sees paused on a healthy gateway learns to ignore the signal, and the
// signal exists to be believed the one time it means something.
func TestEmitterReportsAGapStillInsideTheLagWindow(t *testing.T) {
	db := testDB(t)
	f := newFakeControlPlane(t)
	ctx := context.Background()

	sealFixture(t, db, 4, 4) // one checkpoint covering events 1-4

	// Two fresh events, then delete the first: a gap whose far side was
	// written just now and is therefore well inside any sane lag window.
	for range 2 {
		if _, err := db.Exec(ctx, `
			INSERT INTO audit_events (request_id, timestamp, event_type)
			VALUES ('req-fresh-gap', NOW(), 'test_event')
		`); err != nil {
			t.Fatalf("inserting an audit event: %v", err)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM audit_events WHERE id = 5`); err != nil {
		t.Fatalf("creating the gap: %v", err)
	}

	result, err := runEmitter(t, db, f, 0)
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if result.SealState != controlplanev1.SealStateWaitingOnGap {
		t.Fatalf("seal state is %q, want %q: a gap younger than the lag window has not been "+
			"attempted yet and may still fill", result.SealState, controlplanev1.SealStateWaitingOnGap)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	report := f.statusReports[0]
	if report.GapAgeSeconds == nil {
		t.Fatal("no gap age was reported")
	}
	if *report.GapAgeSeconds >= report.SealLagSeconds {
		t.Errorf("gap age %ds is not inside the declared window of %ds, so this test is not "+
			"exercising the waiting state", *report.GapAgeSeconds, report.SealLagSeconds)
	}
	// The same numbers are reported whatever the state, so an operator can see
	// how far behind a healthy gateway is without waiting for it to break.
	if report.FirstUnsealedEventID == nil || *report.FirstUnsealedEventID != 6 {
		t.Errorf("first_unsealed_event_id is %v, want 6", report.FirstUnsealedEventID)
	}
}

// TestSealStateRespectsADeclaredWindow covers the declaration itself: the same
// database yields a different state depending on the window the gateway says it
// runs with, and the report carries the number it was judged against.
func TestSealStateRespectsADeclaredWindow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	sealFixture(t, db, 4, 4)
	for range 2 {
		if _, err := db.Exec(ctx, `
			INSERT INTO audit_events (request_id, timestamp, event_type)
			VALUES ('req-window', NOW() - INTERVAL '30 seconds', 'test_event')
		`); err != nil {
			t.Fatalf("inserting an audit event: %v", err)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM audit_events WHERE id = 5`); err != nil {
		t.Fatalf("creating the gap: %v", err)
	}

	// A 30-second-old gap: inside a five minute window, beyond a ten second one.
	for _, tc := range []struct {
		lag  int64
		want controlplanev1.SealState
	}{
		{lag: 300, want: controlplanev1.SealStateWaitingOnGap},
		{lag: 10, want: controlplanev1.SealStatePausedAtGap},
	} {
		status, err := checkpoint.ReadSealStatus(ctx, db, tc.lag)
		if err != nil {
			t.Fatalf("reading status with lag %d: %v", tc.lag, err)
		}
		if status.State != tc.want {
			t.Errorf("with a %ds window the state is %q, want %q", tc.lag, status.State, tc.want)
		}
		if status.LagSeconds != tc.lag {
			t.Errorf("the status reports a window of %ds, want the declared %ds",
				status.LagSeconds, tc.lag)
		}
	}
}

// TestSealStateFollowsTheSealerWhenTimestampsAreNotMonotonic pins the state to
// the set the sealer selects from rather than to the age of the gap.
//
// `timestamp` defaults to the inserting transaction's start time, so a long
// transaction commits a row whose timestamp is older than rows already holding
// higher ids. The lowest-id row beyond a gap can therefore be young while a
// higher-id row beyond the same gap has already aged past the watermark. The
// sealer filters on timestamp before ordering by id, so it takes the older row,
// fails its contiguity check and stops — while a state derived from the age of
// the lowest-id row still calls the gateway healthy.
//
// Reporting health for a chain that is not advancing is the failure this signal
// exists to prevent, so the two must agree. The test asserts that agreement by
// running the sealer, not by trusting the reasoning.
func TestSealStateFollowsTheSealerWhenTimestampsAreNotMonotonic(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	sealFixture(t, db, 4, 4) // ids 1-4 sealed

	// id 5 exists only to be removed: it is the hole the sealer refuses to
	// seal past.
	// id 6 is young, so a state judged by gap age calls this healthy.
	// id 7 is old, so it is what the sealer's watermark-filtered batch begins
	// at — beyond the hole, which stops sealing now.
	for _, age := range []string{"1 second", "10 seconds", "1 hour"} {
		if _, err := db.Exec(ctx, `
			INSERT INTO audit_events (request_id, timestamp, event_type)
			VALUES ('req-nonmonotonic', NOW() - $1::interval, 'test_event')
		`, age); err != nil {
			t.Fatalf("inserting an audit event aged %s: %v", age, err)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM audit_events WHERE id = 5`); err != nil {
		t.Fatalf("creating the gap: %v", err)
	}

	const lag = int64(300)

	// What the sealer actually does with this database, established first so
	// the expected state is not an assumption.
	err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(int(lag))})
	if !errors.Is(err, checkpoint.ErrSealPausedAtGap) {
		t.Fatalf("the sealer did not pause at the gap, so this fixture no longer "+
			"exercises the disagreement: %v", err)
	}

	status, err := checkpoint.ReadSealStatus(ctx, db, lag)
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}
	if status.State != controlplanev1.SealStatePausedAtGap {
		t.Errorf("the state is %q while the sealer is stopped at the gap, want %q",
			status.State, controlplanev1.SealStatePausedAtGap)
	}

	// The age stays what it objectively is — measured from the first event
	// beyond the gap — even though the state is no longer derived from it.
	// Reporting a young age alongside paused_at_gap is the honest answer here,
	// and it is what lets a consumer see the non-monotonicity.
	if status.FirstUnsealedEventID == nil || *status.FirstUnsealedEventID != 6 {
		t.Errorf("first_unsealed_event_id is %v, want 6", status.FirstUnsealedEventID)
	}
	if status.GapAge == nil {
		t.Fatal("no gap age reported for a gap")
	}
	if *status.GapAge > time.Duration(lag)*time.Second {
		t.Errorf("gap age is %s, want an age inside the %ds window: the point of the "+
			"fixture is that the age alone would say waiting_on_gap", *status.GapAge, lag)
	}
}
