// Package rules evaluates parsed queries against configurable policy rules.
package rules

import (
	"fmt"
	"time"

	"es-querycost/internal/cost"
	"es-querycost/internal/datemath"
	"es-querycost/internal/query"
)

// Rule is a predicate that can reject a query for a given reason.
type Rule interface {
	// Evaluate inspects the query, cost report and user context and returns an
	// error if the query should be rejected.
	Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) error
}

// Func adapts a plain function to the Rule interface.
type Func func(query string, ast query.Node, report cost.Report, ctx map[string]any) error

func (f Func) Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) error {
	return f(query, ast, report, ctx)
}

// PlanLimit rejects a query when its cost exceeds the user's plan limit.
type PlanLimit struct {
	// Limits is a map of plan name to maximum allowed cost.
	Limits map[string]float64
	// DefaultLimit is used when the plan is not recognised.
	DefaultLimit float64
}

func (r PlanLimit) Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) error {
	plan := PlanFromContext(ctx)
	limit, ok := r.Limits[plan]
	if !ok {
		limit = r.DefaultLimit
	}
	if report.Cost > limit {
		return fmt.Errorf("query cost %.2f exceeds %s plan limit of %.2f", report.Cost, plan, limit)
	}
	return nil
}

// ForbiddenFeature rejects queries that contain forbidden patterns such as
// leading wildcards or match-all terms.
type ForbiddenFeature struct {
	// Forbidden can contain any of: "leading_wildcard", "match_all", "open_range".
	Forbidden []string
}

func (r ForbiddenFeature) Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) error {
	for _, f := range r.Forbidden {
		if report.Breakdown[f] > 0 {
			return fmt.Errorf("forbidden query feature: %s", f)
		}
	}
	return nil
}

// QueryWindow enforces a maximum queryable time window on configured date fields.
type QueryWindow struct {
	// DateFields is the list of field names that represent event time.
	DateFields []string
	// Windows maps plan name to the maximum allowed query window duration.
	Windows map[string]time.Duration
	// DefaultWindow is used for unrecognised plans.
	DefaultWindow time.Duration
	// RequireWindow rejects queries that do not contain a date range on one of
	// the configured fields.
	RequireWindow bool
}

func (r QueryWindow) Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) error {
	plan := PlanFromContext(ctx)
	limit, ok := r.Windows[plan]
	if !ok {
		limit = r.DefaultWindow
	}

	ref := time.Now()
	window, found, err := r.FindWindow(ast, ref)
	if err != nil {
		return err
	}
	if !found {
		if r.RequireWindow {
			return fmt.Errorf("query must include a date window on one of %v", r.DateFields)
		}
		return nil
	}

	d, ok := window.Duration()
	if !ok {
		return fmt.Errorf("date range has an open (unbounded) end")
	}
	if d > limit {
		return fmt.Errorf("query window %v exceeds %s plan limit of %v", d, plan, limit)
	}
	return nil
}

// FindWindow searches the AST for a date window on one of the configured fields.
// It returns the parsed window, whether one was found, and any parse error.
func (r QueryWindow) FindWindow(node query.Node, ref time.Time) (datemath.Window, bool, error) {
	switch n := node.(type) {
	case query.Range:
		w, err := datemath.ParseWindow(n.Low, n.High, ref)
		return w, false, err
	case query.Clause:
		if r.isDateField(n.Field) {
			rng, ok := n.Term.(query.Range)
			if ok {
				w, err := datemath.ParseWindow(rng.Low, rng.High, ref)
				return w, true, err
			}
		}
		return r.FindWindow(n.Term, ref)
	case query.Boolean:
		for _, c := range n.Clauses {
			w, found, err := r.FindWindow(c, ref)
			if err != nil {
				return datemath.Window{}, false, err
			}
			if found {
				return w, true, nil
			}
		}
	case query.Group:
		return r.FindWindow(n.Query, ref)
	}
	return datemath.Window{}, false, nil
}

func (r QueryWindow) isDateField(field string) bool {
	for _, f := range r.DateFields {
		if f == field {
			return true
		}
	}
	return false
}

// Engine evaluates a set of rules against a query.
type Engine struct {
	Rules []Rule
}

// Decision is the outcome of rule evaluation.
type Decision struct {
	Allowed bool
	Reason  string
	Report  cost.Report
}

// Evaluate applies each registered rule in order and returns the first denial.
func (e *Engine) Evaluate(query string, ast query.Node, report cost.Report, ctx map[string]any) Decision {
	for _, rule := range e.Rules {
		if err := rule.Evaluate(query, ast, report, ctx); err != nil {
			return Decision{Allowed: false, Reason: err.Error(), Report: report}
		}
	}
	return Decision{Allowed: true, Report: report}
}

func PlanFromContext(ctx map[string]any) string {
	if ctx == nil {
		return "default"
	}
	user, ok := ctx["user"].(map[string]any)
	if !ok {
		return "default"
	}
	plan, ok := user["plan"].(string)
	if !ok {
		return "default"
	}
	return plan
}

// DefaultEngine returns an engine with sensible defaults.
func DefaultEngine() *Engine {
	return &Engine{
		Rules: []Rule{
			ForbiddenFeature{Forbidden: []string{"match_all", "open_range"}},
			PlanLimit{
				Limits: map[string]float64{
					"free":       50,
					"starter":    200,
					"pro":        1000,
					"enterprise": 10000,
				},
				DefaultLimit: 100,
			},
			QueryWindow{
				DateFields: []string{"@timestamp", "timestamp", "date", "created_at"},
				Windows: map[string]time.Duration{
					"free":       14 * 24 * time.Hour,
					"starter":    30 * 24 * time.Hour,
					"pro":        60 * 24 * time.Hour,
					"enterprise": 365 * 24 * time.Hour,
				},
				DefaultWindow: 30 * 24 * time.Hour,
				RequireWindow: false,
			},
		},
	}
}
