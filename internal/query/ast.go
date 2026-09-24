// Package query parses Lucene-style query strings into an AST.
package query

import (
	"strconv"
	"strings"
)

// Node is the interface implemented by all nodes in the Lucene query AST.
type Node interface {
	isNode()
	// String returns a Lucene query-string representation of the node.
	String() string
}

// Boolean groups clauses with an implicit or explicit boolean operator.
type Boolean struct {
	Clauses []Clause
}

func (Boolean) isNode() {}

func (b Boolean) String() string {
	parts := make([]string, 0, len(b.Clauses))
	for _, c := range b.Clauses {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, " ")
}

// Clause represents a single clause in a boolean query.
type Clause struct {
	// Occur describes how the clause participates in a boolean query.
	// Values: Must (+), MustNot (-), Should (omitted/default).
	Occur Occur
	// Field optionally restricts the clause to a field. Empty means default field.
	Field string
	// Term is the actual query term/group/range/etc.
	Term Node
}

func (Clause) isNode() {}

func (c Clause) String() string {
	var prefix string
	switch c.Occur {
	case Must:
		prefix = "+"
	case MustNot:
		prefix = "-"
	}
	var term string
	if c.Term != nil {
		term = c.Term.String()
	}
	if c.Field != "" {
		return prefix + c.Field + ":" + term
	}
	return prefix + term
}

// Occur describes clause occurrence requirements.
type Occur int

const (
	// Should is the default; clause contributes to score but is not required.
	Should Occur = iota
	// Must requires the clause to match (matches '+', 'AND').
	Must
	// MustNot excludes matches (matches '-', 'NOT').
	MustNot
)

// Term is a bare term or prefix/wildcard term.
type Term struct {
	Value string
	// Wildcard is true when the term contains * or ?.
	Wildcard bool
	// Prefix is true when the term ends with * and contains no other wildcard chars.
	Prefix bool
}

func (Term) isNode() {}

func (t Term) String() string { return t.Value }

// Phrase is a quoted phrase.
type Phrase struct {
	Value string
}

func (Phrase) isNode() {}

func (p Phrase) String() string { return strconv.Quote(p.Value) }

// Range is a range query such as [1 TO 10] or {a TO z}.
type Range struct {
	Low           string
	High          string
	InclusiveLow  bool
	InclusiveHigh bool
}

func (Range) isNode() {}

func (r Range) String() string {
	low := r.Low
	if low == "" {
		low = "*"
	}
	high := r.High
	if high == "" {
		high = "*"
	}
	open := "["
	if !r.InclusiveLow {
		open = "{"
	}
	close := "]"
	if !r.InclusiveHigh {
		close = "}"
	}
	return open + low + " TO " + high + close
}

// Group is a parenthesised sub-query.
type Group struct {
	Query Node
}

func (Group) isNode() {}

func (g Group) String() string {
	if g.Query == nil {
		return "()"
	}
	return "(" + g.Query.String() + ")"
}
