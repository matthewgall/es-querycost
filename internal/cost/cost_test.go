package cost

import (
	"testing"

	"es-querycost/internal/query"
)

func estimateString(t *testing.T, q string) Report {
	t.Helper()
	ast, err := query.Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	return Estimate(ast, DefaultModel)
}

func TestCostCheapExact(t *testing.T) {
	r := estimateString(t, `asn:AS13335`)
	if r.Cost <= 0 {
		t.Errorf("expected positive cost, got %f", r.Cost)
	}
	if r.Cost > 10 {
		t.Errorf("exact term on low-cardinality field should be cheap, got %f", r.Cost)
	}
}

func TestCostUserExample(t *testing.T) {
	r := estimateString(t, `asn:AS13335 AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`)
	if r.Cost <= 50 {
		t.Errorf("expected expensive wildcard query to cost more than 50, got %f", r.Cost)
	}
	if r.Breakdown["leading_wildcard_keyword"] == 0 {
		t.Errorf("expected leading wildcard keyword penalty")
	}
}

func TestCostMatchAll(t *testing.T) {
	r := estimateString(t, `*:*`)
	if r.Cost < 100 {
		t.Errorf("expected match-all to be very expensive, got %f", r.Cost)
	}
}

func TestCostOpenRange(t *testing.T) {
	r := estimateString(t, `date:[* TO *]`)
	if r.Breakdown["open_range"] == 0 {
		t.Errorf("expected open range penalty")
	}
}

func TestCostOrPenalty(t *testing.T) {
	r := estimateString(t, `a:1 OR b:2 OR c:3 OR d:4`)
	if r.Breakdown["or_clauses"] == 0 {
		t.Errorf("expected OR clause penalty")
	}
}

func TestCostBooleanMustUsesMax(t *testing.T) {
	cheap := estimateString(t, `asn:AS13335 AND asn:AS13335`)
	if cheap.Cost > 10 {
		t.Errorf("AND of two cheap exact terms should stay cheap, got %f", cheap.Cost)
	}
}

func TestCostDefaultFieldPenalty(t *testing.T) {
	withField := estimateString(t, `asn:AS13335`)
	withoutField := estimateString(t, `AS13335`)
	if withoutField.Cost <= withField.Cost {
		t.Errorf("default-field term should be more expensive than targeted term: withField=%f withoutField=%f", withField.Cost, withoutField.Cost)
	}
}

func TestCostFieldWeights(t *testing.T) {
	model := DefaultModel
	model.FieldWeights = map[string]float64{
		"expensive_field": 10,
	}
	ast, _ := query.Parse(`expensive_field:value`)
	r := Estimate(ast, model)
	if r.Cost < 10 {
		t.Errorf("weighted field should increase cost, got %f", r.Cost)
	}
}

func TestCostPhraseScalesWithTermCount(t *testing.T) {
	short := estimateString(t, `title:"quick brown"`)
	long := estimateString(t, `title:"the quick brown fox jumps"`)
	if long.Cost <= short.Cost {
		t.Errorf("longer phrase should cost more: short=%f long=%f", short.Cost, long.Cost)
	}
}

func TestCostMustNotPenalty(t *testing.T) {
	without := estimateString(t, `asn:AS13335`)
	withNot := estimateString(t, `asn:AS13335 AND NOT deleted:true`)
	if withNot.Cost <= without.Cost {
		t.Errorf("MUST_NOT should add penalty: without=%f with=%f", without.Cost, withNot.Cost)
	}
}
