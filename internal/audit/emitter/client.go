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

// Package emitter submits sealed audit checkpoints to a control plane.
//
// It speaks api/controlplane/v1, which is a published protocol, so this works
// against any implementation of it and not only against a particular vendor's.
//
// What it sends is what the sealer wrote: Merkle roots, chain hashes, ranges of
// event IDs, and the parameters needed to recompute a checkpoint hash. It sends
// no audit event and no request or response content, because the protocol has
// no field to carry any.
//
// Why the gateway submits at all: docs/adr/0006 records that the checkpoint
// hash does not cover a checkpoint's predecessor identity, so this gateway's
// own offline verifier cannot detect a chain that has been repointed. Only a
// party that witnessed the original ordering can. Submitting each checkpoint as
// it is sealed is what creates that witness.
package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
)

// maxErrorBodyBytes bounds how much of an error response is read. A control
// plane returns a small JSON object; anything larger is a misconfigured proxy,
// and reading it into a log line is how a disk fills.
const maxErrorBodyBytes = 64 << 10

// Client speaks the control plane protocol over HTTP.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewClient returns a client for the control plane at endpoint, authenticating
// with token.
//
// The token is held in memory for the life of the process and is never written
// to the database or to a log line.
func NewClient(endpoint, token string, timeout time.Duration) (*Client, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("control plane endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		return nil, fmt.Errorf("control plane endpoint %q must be an http or https URL", endpoint)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("control plane token is required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: timeout}}, nil
}

// RegisterGateway registers this deployment and returns the assigned identity.
//
// Registration is idempotent on name within an organization, so a gateway that
// has registered before receives the same identity rather than a second one.
func (c *Client) RegisterGateway(ctx context.Context, name, gatewayVersion string) (*controlplanev1.GatewayRegistrationResponse, error) {
	req := controlplanev1.GatewayRegistration{Name: name, GatewayVersion: gatewayVersion}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("registration is not valid: %w", err)
	}

	var resp controlplanev1.GatewayRegistrationResponse
	if err := c.post(ctx, "/v1/gateways", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SubmitCheckpoint submits one checkpoint.
//
// The submission is validated locally first. A message this gateway knows to be
// malformed is not worth a round trip, and the local error names the field
// while a remote rejection has to be parsed back out of a response.
func (c *Client) SubmitCheckpoint(ctx context.Context, sub *controlplanev1.CheckpointSubmission) (*controlplanev1.CheckpointSubmissionResponse, error) {
	if err := sub.Validate(); err != nil {
		return nil, fmt.Errorf("checkpoint %d is not valid: %w", sub.CheckpointID, err)
	}
	// Recompute before sending. If this gateway cannot reproduce its own
	// checkpoint hash, the checkpoint is already unverifiable and transmitting
	// it would only move the problem somewhere it is harder to diagnose.
	if err := controlplanev1.VerifyCheckpointHash(sub); err != nil {
		return nil, fmt.Errorf("refusing to submit an unverifiable checkpoint: %w", err)
	}

	var resp controlplanev1.CheckpointSubmissionResponse
	if err := c.post(ctx, "/v1/checkpoints", sub, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RemoteError is a non-2xx response from the control plane.
//
// It carries the protocol's own error body when there is one, so a chain
// discontinuity reaches the operator with the sequences and hashes in it rather
// than as a status code.
type RemoteError struct {
	StatusCode int
	Body       controlplanev1.Error
	Raw        string
}

// Error implements the error interface.
func (e *RemoteError) Error() string {
	if e.Body.Code != "" {
		return fmt.Sprintf("control plane returned %d %s: %s",
			e.StatusCode, e.Body.Code, e.Body.Message)
	}
	return fmt.Sprintf("control plane returned %d: %s", e.StatusCode, e.Raw)
}

// IsChainDiscontinuity reports whether the control plane refused this
// checkpoint because it does not join the chain it already holds.
//
// This is worth distinguishing from a transport failure. A timeout is retried;
// a chain discontinuity means the two sides disagree about what has been
// recorded, and retrying cannot resolve it.
func (e *RemoteError) IsChainDiscontinuity() bool {
	switch e.Body.Code {
	case controlplanev1.ErrCodeChainGap,
		controlplanev1.ErrCodeChainFork,
		controlplanev1.ErrCodeChainPrevMismatch,
		controlplanev1.ErrCodeChainGenesisConflict:
		return true
	}
	return false
}

// post sends a JSON request and decodes a JSON response.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding the request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling the control plane at %s: %w", c.endpoint+path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		remote := &RemoteError{StatusCode: resp.StatusCode, Raw: strings.TrimSpace(string(raw))}
		// A body that is not the protocol's error shape is left in Raw. A
		// proxy or a load balancer can answer instead of the control plane,
		// and its HTML should not be mistaken for a protocol response.
		_ = json.Unmarshal(raw, &remote.Body)
		return remote
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodyBytes)).Decode(out); err != nil {
		return fmt.Errorf("decoding the response from %s: %w", c.endpoint+path, err)
	}
	return nil
}
