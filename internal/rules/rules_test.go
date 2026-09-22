package rules

import (
	"testing"

	"es-querycost/internal/cost"
	"es-querycost/internal/query"
)

func evaluate(t *testing.T, q string, ctx map[string]any) Decision {
	t.Helper()
	ast, err := query.Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	report := cost.Estimate(ast, cost.DefaultModel)
	return DefaultEngine().Evaluate(q, ast, report, ctx)
}

func TestRuleAllowsFreeExact(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-14d TO now]`, map[string]any{
		"user": map[string]any{"plan": "free"},
	})
	if !dec.Allowed {
		t.Errorf("expected allowed, got %s", dec.Reason)
	}
}

func TestRuleRejectsFreeHeavy(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-14d TO now] AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`, map[string]any{
		"user": map[string]any{"plan": "free"},
	})
	if dec.Allowed {
		t.Errorf("expected denial for free plan")
	}
}

func TestRuleAllowsProHeavy(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-60d TO now] AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`, map[string]any{
		"user": map[string]any{"plan": "pro"},
	})
	if !dec.Allowed {
		t.Errorf("expected pro plan to allow, got %s", dec.Reason)
	}
}

func TestRuleRejectsMatchAll(t *testing.T) {
	dec := evaluate(t, `*:*`, map[string]any{
		"user": map[string]any{"plan": "enterprise"},
	})
	if dec.Allowed {
		t.Errorf("expected match-all to be rejected")
	}
}

func TestRuleRejectsWindowTooLarge(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-30d TO now]`, map[string]any{
		"user": map[string]any{"plan": "free"},
	})
	if dec.Allowed {
		t.Errorf("expected 30 day window to be rejected for free plan")
	}
}

func TestRuleAllowsMaxWindow(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-14d TO now]`, map[string]any{
		"user": map[string]any{"plan": "free"},
	})
	if !dec.Allowed {
		t.Errorf("expected 14 day window to be allowed for free plan, got %s", dec.Reason)
	}
}

func TestRuleDateField(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND date:[now-7d TO now]`, map[string]any{
		"user": map[string]any{"plan": "free"},
	})
	if !dec.Allowed {
		t.Errorf("expected date field to be recognised, got %s", dec.Reason)
	}
}

func TestRuleUnknownPlan(t *testing.T) {
	dec := evaluate(t, `asn:AS13335 AND @timestamp:[now-7d TO now]`, map[string]any{
		"user": map[string]any{"plan": "mystery"},
	})
	if !dec.Allowed {
		t.Errorf("expected default limits to allow small query, got %s", dec.Reason)
	}
}

func TestPlanFromContext(t *testing.T) {
	if got := PlanFromContext(nil); got != "default" {
		t.Errorf("nil context: got %s, want default", got)
	}
	if got := PlanFromContext(map[string]any{}); got != "default" {
		t.Errorf("empty context: got %s, want default", got)
	}
	if got := PlanFromContext(map[string]any{
		"user": map[string]any{"plan": "pro"},
	}); got != "pro" {
		t.Errorf("got %s, want pro", got)
	}
}
