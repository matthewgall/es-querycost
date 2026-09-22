// Package cost estimates the execution cost of a parsed Lucene query.
package cost

import (
	"math"
	"strings"

	"es-querycost/internal/query"
)

// Model assigns cost penalties to different query features.
// Higher values mean more expensive queries.
type Model struct {
	TermCost                  float64
	PhraseCost                float64
	WildcardMultiplier        float64
	LeadingWildcardCost       float64
	MatchAllCost              float64
	RangeCost                 float64
	OpenRangeCost             float64
	OrClauseCost              float64
	DepthCost                 float64
	KeywordWildcardMultiplier float64
	// FieldWeights allows per-field cost scaling. A field with high cardinality
	// or poor indexing should have a higher weight.
	FieldWeights map[string]float64
	// DefaultFieldWeight is used when a field has no explicit weight.
	DefaultFieldWeight float64
	// DefaultSearchFieldPenalty scales terms that do not target a specific
	// field (i.e. queries against the implicit default field).
	DefaultSearchFieldPenalty float64
}

// DefaultModel is tuned so that the example heavy query clearly exceeds a
// free-plan limit while cheap exact queries stay well below it.
var DefaultModel = Model{
	TermCost:                  1,
	PhraseCost:                2,
	WildcardMultiplier:        10,
	LeadingWildcardCost:       50,
	MatchAllCost:              100,
	RangeCost:                 5,
	OpenRangeCost:             50,
	OrClauseCost:              3,
	DepthCost:                 1,
	KeywordWildcardMultiplier: 3,
	FieldWeights: map[string]float64{
		"asn": 0.5, // low cardinality, cheap
	},
	DefaultFieldWeight:        1,
	DefaultSearchFieldPenalty: 2,
}

// Report is the result of analysing a query.
type Report struct {
	Cost        float64
	Breakdown   map[string]float64
	Explanation []string
}

// Estimate calculates the cost of a parsed query using the supplied model.
func Estimate(node query.Node, model Model) Report {
	r := Report{
		Cost:        0,
		Breakdown:   make(map[string]float64),
		Explanation: []string{},
	}
	r.Cost = estimate(node, model, "", 0, &r)
	r.Cost = Round(r.Cost)
	return r
}

func estimate(node query.Node, model Model, field string, depth int, r *Report) float64 {
	switch n := node.(type) {
	case query.Term:
		return termCost(n, model, field, r)
	case query.Phrase:
		return phraseCost(n, model, field, r)
	case query.Range:
		return rangeCost(n, model, field, r)
	case query.Group:
		depthBonus := model.DepthCost * float64(depth+1)
		add(r, "depth", depthBonus)
		return estimate(n.Query, model, field, depth+1, r) + depthBonus
	case query.Boolean:
		return booleanCost(n, model, depth, r)
	case query.Clause:
		return clauseCost(n, model, depth, r)
	default:
		return 0
	}
}

func clauseCost(c query.Clause, model Model, depth int, r *Report) float64 {
	field := c.Field
	weight := fieldWeight(model, field)
	child := estimate(c.Term, model, field, depth, r) * weight
	if c.Occur == query.MustNot {
		add(r, "must_not", 2)
		child += 2
	}
	return child
}

func booleanCost(b query.Boolean, model Model, depth int, r *Report) float64 {
	var mustCost, shouldCost float64
	var shouldCount int

	for _, c := range b.Clauses {
		child := clauseCost(c, model, depth, r)
		switch c.Occur {
		case query.Must:
			mustCost = math.Max(mustCost, child)
		case query.Should:
			shouldCount++
			shouldCost += child
		case query.MustNot:
			// MUST_NOT adds a fixed penalty rather than dominating the cost.
			mustCost += child * 0.1
		}
	}

	cost := mustCost + shouldCost
	if shouldCount > 0 {
		penalty := float64(shouldCount) * model.OrClauseCost
		add(r, "or_clauses", penalty)
		cost += penalty
	}
	return cost
}

func termCost(n query.Term, model Model, field string, r *Report) float64 {
	if n.Value == "*" {
		add(r, "match_all", model.MatchAllCost)
		return model.MatchAllCost
	}

	base := model.TermCost * fieldWeight(model, field)
	if field == "" {
		base *= model.DefaultSearchFieldPenalty
	}

	if !n.Wildcard {
		add(r, "term", base)
		return base
	}

	cost := base * model.WildcardMultiplier
	rationale := "wildcard"

	if strings.HasPrefix(n.Value, "*") || strings.HasPrefix(n.Value, "?") {
		cost += model.LeadingWildcardCost
		rationale = "leading_wildcard"
	}
	if n.Prefix {
		rationale = "prefix_wildcard"
	}

	if isKeywordField(field) {
		cost *= model.KeywordWildcardMultiplier
		rationale += "_keyword"
	}

	add(r, rationale, cost)
	return cost
}

func phraseCost(n query.Phrase, model Model, field string, r *Report) float64 {
	// Phrase cost scales with the number of terms in the phrase and the field
	// weight, making long phrases on heavy fields more expensive.
	termCount := float64(len(strings.Fields(n.Value)))
	if termCount < 1 {
		termCount = 1
	}
	base := model.PhraseCost * termCount * fieldWeight(model, field)
	if field == "" {
		base *= model.DefaultSearchFieldPenalty
	}
	add(r, "phrase", base)
	return base
}

func rangeCost(n query.Range, model Model, field string, r *Report) float64 {
	weight := fieldWeight(model, field)
	if (n.Low == "*" || n.Low == "") && (n.High == "*" || n.High == "") {
		cost := model.OpenRangeCost * weight
		add(r, "open_range", cost)
		return cost
	}
	cost := model.RangeCost * weight
	if field == "" {
		cost *= model.DefaultSearchFieldPenalty
	}
	add(r, "range", cost)
	return cost
}

func add(r *Report, key string, cost float64) {
	r.Breakdown[key] += cost
	r.Explanation = append(r.Explanation, key)
}

func isKeywordField(field string) bool {
	return strings.HasSuffix(field, ".keyword")
}

func fieldWeight(model Model, field string) float64 {
	if field == "" {
		return 1
	}
	if w, ok := model.FieldWeights[field]; ok {
		return w
	}
	return model.DefaultFieldWeight
}

// Round rounds a cost to two decimals for display.
func Round(cost float64) float64 {
	return math.Round(cost*100) / 100
}
