package query

import (
	"testing"
)

func TestParseSimpleTerm(t *testing.T) {
	q, err := Parse(`asn:AS13335`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c, ok := q.(Clause)
	if !ok {
		t.Fatalf("expected Clause, got %T", q)
	}
	if c.Field != "asn" {
		t.Errorf("expected field asn, got %s", c.Field)
	}
	term, ok := c.Term.(Term)
	if !ok {
		t.Fatalf("expected Term, got %T", c.Term)
	}
	if term.Value != "AS13335" {
		t.Errorf("expected AS13335, got %s", term.Value)
	}
}

func TestParseUserExample(t *testing.T) {
	q, err := Parse(`asn:AS13335 AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	bq, ok := q.(Boolean)
	if !ok {
		t.Fatalf("expected Boolean, got %T", q)
	}
	if len(bq.Clauses) != 2 {
		t.Fatalf("expected 2 clauses, got %d", len(bq.Clauses))
	}
	if bq.Clauses[0].Occur != Must {
		t.Errorf("expected first clause Must")
	}
	grp, ok := bq.Clauses[1].Term.(Group)
	if !ok {
		t.Fatalf("expected group, got %T", bq.Clauses[1].Term)
	}
	inner, ok := grp.Query.(Boolean)
	if !ok {
		t.Fatalf("expected inner boolean, got %T", grp.Query)
	}
	if len(inner.Clauses) != 3 {
		t.Fatalf("expected 3 inner clauses, got %d", len(inner.Clauses))
	}
}

func TestParseWildcards(t *testing.T) {
	q, err := Parse(`page.url.keyword:*lidl*`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c := q.(Clause)
	term := c.Term.(Term)
	if !term.Wildcard {
		t.Errorf("expected wildcard")
	}
	if term.Prefix {
		t.Errorf("did not expect prefix")
	}
}

func TestParsePrefix(t *testing.T) {
	q, err := Parse(`name:John*`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	term := q.(Clause).Term.(Term)
	if !term.Prefix || !term.Wildcard {
		t.Errorf("expected prefix wildcard")
	}
}

func TestParseRange(t *testing.T) {
	q, err := Parse(`age:[18 TO 99]`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c := q.(Clause)
	rng, ok := c.Term.(Range)
	if !ok {
		t.Fatalf("expected Range, got %T", c.Term)
	}
	if rng.Low != "18" || rng.High != "99" {
		t.Errorf("unexpected range bounds: %s TO %s", rng.Low, rng.High)
	}
}

func TestParsePhrase(t *testing.T) {
	q, err := Parse(`title:"quick brown"`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	phrase := q.(Clause).Term.(Phrase)
	if phrase.Value != "quick brown" {
		t.Errorf("unexpected phrase: %s", phrase.Value)
	}
}

func TestParseMatchAll(t *testing.T) {
	q, err := Parse(`*:*`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c := q.(Clause)
	term := c.Term.(Term)
	if term.Value != "*" {
		t.Errorf("expected *, got %s", term.Value)
	}
}

func TestParseModifier(t *testing.T) {
	q, err := Parse(`+must:term -not:term`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	bq := q.(Boolean)
	if bq.Clauses[0].Occur != Must {
		t.Errorf("expected Must, got %v", bq.Clauses[0].Occur)
	}
	if bq.Clauses[1].Occur != MustNot {
		t.Errorf("expected MustNot, got %v", bq.Clauses[1].Occur)
	}
}

func TestParseNot(t *testing.T) {
	q, err := Parse(`NOT field:value`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c := q.(Clause)
	if c.Occur != MustNot {
		t.Errorf("expected MustNot, got %v", c.Occur)
	}
}

func TestParseEmpty(t *testing.T) {
	q, err := Parse(``)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if _, ok := q.(Boolean); !ok {
		t.Errorf("expected empty Boolean, got %T", q)
	}
}

func TestParseInvalid(t *testing.T) {
	_, err := Parse(`field:`)
	if err == nil {
		t.Error("expected error for empty term")
	}
}
