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
	"github.com/open-policy-agent/opa/rego"
)

// PolicyInput is the data sent to OPA for evaluation.
type PolicyInput struct {
	User    PolicyUser    `json:"user"`
	Request PolicyReq    `json:"request"`
	Time    PolicyTime   `json:"time"`
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

// Evaluator implements filter.Filter using OPA.
type Evaluator struct {
	mu       sync.RWMutex
	prepared *rego.PreparedEvalQuery
	cfg      func() config.PolicyFilterConfig
}

// NewEvaluator creates a policy evaluator. Call Load() to compile policies.
func NewEvaluator(cfg func() config.PolicyFilterConfig) *Evaluator {
	return &Evaluator{cfg: cfg}
}

func (e *Evaluator) Name() string  { return "policy" }
func (e *Evaluator) Enabled() bool { return e.cfg().Enabled }

// Load compiles Rego modules from the bundle path.
func (e *Evaluator) Load() error {
	cfg := e.cfg()
	modules, err := LoadRegoFiles(cfg.BundlePath)
	if err != nil {
		return fmt.Errorf("load rego files: %w", err)
	}
	if len(modules) == 0 {
		slog.Warn("no rego files found", "path", cfg.BundlePath)
		return nil
	}

	opts := []func(*rego.Rego){
		rego.Query("data.aegis.policy.allow"),
		rego.Query("data.aegis.policy.reason"),
	}
	for name, src := range modules {
		opts = append(opts, rego.Module(name, src))
	}

	// Build with two queries for allow and reason
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

	slog.Info("opa policies loaded", "modules", len(modules))
	return nil
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
	input := PolicyInput{
		User: PolicyUser{
			ID:   req.UserID,
			Org:  req.OrganizationID,
			Team: req.TeamID,
		},
		Request: PolicyReq{
			Model:          req.Model,
			Classification: string(req.Classification),
		},
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
