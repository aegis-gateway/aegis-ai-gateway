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

package policy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"github.com/open-policy-agent/opa/v1/rego"
)

// PolicyMessage represents a single message for policy evaluation.
//
// Content is flattened to a single string because that is the shape existing
// rules match on. Parts is the structured form when the message carried a
// content array, so a rule that needs per-part granularity has it without the
// flattening ambiguity. ToolCalls carries the names of tools this turn asked
// for, never their arguments.
type PolicyMessage struct {
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	Parts     []string `json:"parts,omitempty"`
	ToolCalls []string `json:"tool_calls,omitempty"`
}

// PolicyInput is the data sent to OPA for evaluation.
type PolicyInput struct {
	User     PolicyUser      `json:"user"`
	Request  PolicyReq       `json:"request"`
	Messages []PolicyMessage `json:"messages"`
	Time     PolicyTime      `json:"time"`
}

type PolicyUser struct {
	ID   string `json:"id"`
	Org  string `json:"org"`
	Team string `json:"team"`
}

// PolicyReq is the request-level policy input.
//
// ToolsOffered and ToolsCalled are names only. A tool name says which
// capability was put in front of the model; the arguments say what was done
// with it, and those are payload. Exposing the names makes agent governance
// rules writable ("this key may not be offered a shell tool") without putting
// payload anywhere a policy, a log line, or an audit row could capture it.
//
// No tool-level enforcement rule ships in configs/policies. This is the seam,
// not the policy.
type PolicyReq struct {
	Model          string   `json:"model"`
	Classification string   `json:"classification"`
	ProviderType   string   `json:"provider_type"`
	Stream         bool     `json:"stream"`
	ToolsOffered   []string `json:"tools_offered"`
	ToolsCalled    []string `json:"tools_called"`
	ToolChoice     string   `json:"tool_choice,omitempty"`
}

type PolicyTime struct {
	Hour int    `json:"hour"`
	Day  string `json:"day"`
}

// ReloadMetrics is an optional interface for recording policy reload outcomes.
type ReloadMetrics interface {
	RecordPolicyReload(success bool)
}

// Evaluator implements filter.Filter using OPA.
type Evaluator struct {
	mu       sync.RWMutex
	prepared *rego.PreparedEvalQuery
	cfg      func() config.PolicyFilterConfig
	metrics  ReloadMetrics
}

// NewEvaluator creates a policy evaluator. Call Load() to compile policies.
func NewEvaluator(cfg func() config.PolicyFilterConfig) *Evaluator {
	return &Evaluator{cfg: cfg}
}

// SetMetrics attaches a metrics recorder for policy reload events.
func (e *Evaluator) SetMetrics(m ReloadMetrics) {
	e.metrics = m
}

func (e *Evaluator) Name() string  { return "policy" }
func (e *Evaluator) Enabled() bool { return e.cfg().Enabled }

// Load compiles Rego modules from the bundle path.
// On success, the new query atomically replaces the old one.
// On failure, the existing query is left untouched so evaluation continues
// with the last known-good policy.
func (e *Evaluator) Load() error {
	cfg := e.cfg()
	modules, err := LoadRegoFiles(cfg.BundlePath)
	if err != nil {
		e.recordReload(false)
		return fmt.Errorf("load rego files: %w", err)
	}
	if len(modules) == 0 {
		slog.Warn("no rego files found, clearing policies", "path", cfg.BundlePath)
		e.mu.Lock()
		e.prepared = nil
		e.mu.Unlock()
		e.recordReload(true)
		return nil
	}

	// Compile into a new PreparedEvalQuery first — if this fails,
	// e.prepared is never touched (atomic swap).
	r := rego.New(
		rego.Query("[data.aegis.policy.allow, data.aegis.policy.reason]"),
		func() func(*rego.Rego) {
			mods := make([]func(*rego.Rego), 0, len(modules))
			for name, src := range modules {
				mods = append(mods, rego.Module(name, src))
			}
			return func(r *rego.Rego) {
				for _, m := range mods {
					m(r)
				}
			}
		}(),
	)

	prepared, err := r.PrepareForEval(context.Background())
	if err != nil {
		slog.Error("opa policy compile failed — keeping previous policies",
			"error", err, "path", cfg.BundlePath)
		e.recordReload(false)
		return fmt.Errorf("prepare rego: %w", err)
	}

	// Swap only after successful compilation.
	e.mu.Lock()
	e.prepared = &prepared
	e.mu.Unlock()

	slog.Info("opa policies loaded", "modules", len(modules))
	e.recordReload(true)
	return nil
}

func (e *Evaluator) recordReload(success bool) {
	if e.metrics != nil {
		e.metrics.RecordPolicyReload(success)
	}
}

// LoadFromModules compiles policies from provided module sources (useful for testing).
func (e *Evaluator) LoadFromModules(modules map[string]string) error {
	r := rego.New(
		rego.Query("[data.aegis.policy.allow, data.aegis.policy.reason]"),
		func() func(*rego.Rego) {
			mods := make([]func(*rego.Rego), 0, len(modules))
			for name, src := range modules {
				mods = append(mods, rego.Module(name, src))
			}
			return func(r *rego.Rego) {
				for _, m := range mods {
					m(r)
				}
			}
		}(),
	)

	prepared, err := r.PrepareForEval(context.Background())
	if err != nil {
		return fmt.Errorf("prepare rego: %w", err)
	}

	e.mu.Lock()
	e.prepared = &prepared
	e.mu.Unlock()
	return nil
}

// Evaluate runs the policy against the given input.
func (e *Evaluator) Evaluate(ctx context.Context, input PolicyInput) (bool, string, error) {
	e.mu.RLock()
	prepared := e.prepared
	e.mu.RUnlock()

	if prepared == nil {
		// No policies loaded — fail closed
		return false, "no policies loaded", nil
	}

	cfg := e.cfg()
	timeout := cfg.EvaluationTimeout
	if timeout == 0 {
		timeout = 100 * time.Millisecond
	}

	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results, err := prepared.Eval(evalCtx, rego.EvalInput(input))
	if err != nil {
		return false, fmt.Sprintf("policy evaluation error: %v", err), err
	}

	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return false, "no policy result", nil
	}

	// Result is [allow, reason]
	arr, ok := results[0].Expressions[0].Value.([]interface{})
	if !ok || len(arr) < 2 {
		return false, "unexpected policy result format", nil
	}

	allowed, _ := arr[0].(bool)
	reason, _ := arr[1].(string)

	return allowed, reason, nil
}

// ScanRequest implements filter.Filter.
func (e *Evaluator) ScanRequest(ctx context.Context, req *types.AegisRequest) filter.Result {
	now := time.Now().UTC()

	msgs := make([]PolicyMessage, len(req.Messages))
	for i, m := range req.Messages {
		pm := PolicyMessage{Role: m.Role, Content: m.Content.Flatten()}
		if m.Content.Kind == types.ContentParts {
			pm.Parts = m.Content.Texts()
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				pm.ToolCalls = append(pm.ToolCalls, tc.Function.Name)
			}
		}
		msgs[i] = pm
	}

	input := PolicyInput{
		User: PolicyUser{
			ID:   req.UserID,
			Org:  req.OrganizationID,
			Team: req.TeamID,
		},
		Request: PolicyReq{
			Model:          req.Model,
			Classification: string(req.Classification),
			ProviderType:   req.ProviderType,
			Stream:         req.Stream,
			ToolsOffered:   req.ToolNames(),
			ToolsCalled:    req.CalledToolNames(),
			ToolChoice:     req.ToolChoice.String(),
		},
		Messages: msgs,
		Time: PolicyTime{
			Hour: now.Hour(),
			Day:  now.Weekday().String(),
		},
	}

	allowed, reason, err := e.Evaluate(ctx, input)
	if err != nil {
		slog.Error("policy evaluation failed", "error", err)
		// Fail closed
		return filter.Result{
			Action:     filter.ActionBlock,
			FilterName: "policy",
			Message:    "Policy evaluation failed: " + err.Error(),
		}
	}

	if !allowed {
		return filter.Result{
			Action:     filter.ActionBlock,
			FilterName: "policy",
			Message:    "Request denied by policy: " + reason,
		}
	}

	return filter.Result{Action: filter.ActionPass, FilterName: "policy"}
}
