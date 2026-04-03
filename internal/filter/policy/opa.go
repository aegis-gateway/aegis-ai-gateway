package policy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/types"
	"github.com/open-policy-agent/opa/v1/rego"
)

// PolicyMessage represents a single message for policy evaluation.
type PolicyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

type PolicyReq struct {
	Model          string `json:"model"`
	Classification string `json:"classification"`
	ProviderType   string `json:"provider_type"`
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
		slog.Warn("no rego files found", "path", cfg.BundlePath)
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
		msgs[i] = PolicyMessage{Role: m.Role, Content: m.Content}
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
