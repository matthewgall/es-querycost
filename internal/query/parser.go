package query

import (
	"fmt"
	"strings"
	"unicode"
)

// Parse parses a Lucene-style query string into an AST.
func Parse(input string) (Node, error) {
	p := &parser{input: input, pos: 0}
	p.skipSpace()
	if p.peek() == eof {
		return Boolean{}, nil
	}
	q, err := p.parseBoolean()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != eof {
		return nil, fmt.Errorf("unexpected token at position %d: %q", p.pos, p.input[p.pos:])
	}
	return q, nil
}

const eof = rune(0)

type parser struct {
	input string
	pos   int
}

func (p *parser) peek() rune {
	if p.pos >= len(p.input) {
		return eof
	}
	return rune(p.input[p.pos])
}

func (p *parser) next() rune {
	if p.pos >= len(p.input) {
		return eof
	}
	r := rune(p.input[p.pos])
	p.pos++
	return r
}

func (p *parser) skipSpace() {
	for {
		r := p.peek()
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			p.next()
			continue
		}
		break
	}
}

func (p *parser) parseBoolean() (Node, error) {
	var clauses []Clause
	// pending operator that applies to the next clause: Should (default), Must (after AND)
	nextOccur := Should
	for {
		p.skipSpace()
		if p.peek() == eof || p.peek() == ')' {
			break
		}
		clause, err := p.parseClause()
		if err != nil {
			return nil, err
		}
		if nextOccur == Must {
			clause.Occur = Must
			if len(clauses) > 0 {
				clauses[len(clauses)-1].Occur = Must
			}
		}
		clauses = append(clauses, clause)
		nextOccur = Should

		p.skipSpace()
		r := p.peek()
		if r == eof || r == ')' {
			break
		}
		if strings.HasPrefix(strings.ToUpper(p.rest()), "AND") {
			p.consumeWord("AND")
			nextOccur = Must
			continue
		}
		if strings.HasPrefix(strings.ToUpper(p.rest()), "OR") {
			p.consumeWord("OR")
			nextOccur = Should
			continue
		}
	}
	if len(clauses) == 1 {
		c := clauses[0]
		if c.Field == "" {
			return c.Term, nil
		}
		return c, nil
	}
	return Boolean{Clauses: clauses}, nil
}

func (p *parser) rest() string {
	return p.input[p.pos:]
}

func (p *parser) consumeWord(word string) {
	p.pos += len(word)
}

func (p *parser) parseClause() (Clause, error) {
	var occur Occur = Should
	p.skipSpace()
	r := p.peek()
	if r == '+' {
		occur = Must
		p.next()
	} else if r == '-' {
		occur = MustNot
		p.next()
	} else if strings.HasPrefix(strings.ToUpper(p.rest()), "NOT ") {
		p.consumeWord("NOT")
		occur = MustNot
		p.skipSpace()
	}

	field, err := p.parseField()
	if err != nil {
		return Clause{}, err
	}

	node, err := p.parseTerm()
	if err != nil {
		return Clause{}, err
	}

	return Clause{Occur: occur, Field: field, Term: node}, nil
}

func (p *parser) parseField() (string, error) {
	start := p.pos
	for {
		r := p.peek()
		if r == eof || r == ':' || unicode.IsSpace(r) || isSpecial(r) {
			break
		}
		p.next()
	}
	field := p.input[start:p.pos]
	if p.peek() == ':' {
		p.next()
		return field, nil
	}
	p.pos = start
	return "", nil
}

func (p *parser) parseTerm() (Node, error) {
	p.skipSpace()
	r := p.peek()
	switch r {
	case eof:
		return nil, fmt.Errorf("unexpected end of query")
	case '"':
		return p.parsePhrase()
	case '[':
		return p.parseRange(true)
	case '{':
		return p.parseRange(false)
	case '(':
		return p.parseGroup()
	default:
		return p.parseWord()
	}
}

func (p *parser) parsePhrase() (Node, error) {
	p.next() // consume opening quote
	start := p.pos
	for {
		r := p.next()
		if r == eof {
			return nil, fmt.Errorf("unterminated phrase starting at %d", start)
		}
		if r == '"' {
			break
		}
		if r == '\\' {
			p.next()
		}
	}
	value := p.input[start : p.pos-1]
	return Phrase{Value: value}, nil
}

func (p *parser) parseRange(incl bool) (Node, error) {
	p.next() // consume [ or {
	p.skipSpace()
	low, err := p.parseRangeToken()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if !strings.HasPrefix(strings.ToUpper(p.rest()), "TO") {
		return nil, fmt.Errorf("expected TO in range at %d", p.pos)
	}
	p.consumeWord("TO")
	p.skipSpace()
	high, err := p.parseRangeToken()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	closing := p.next()
	if (incl && closing != ']') || (!incl && closing != '}') {
		return nil, fmt.Errorf("unterminated range at %d", p.pos)
	}
	return Range{Low: low, High: high, InclusiveLow: incl, InclusiveHigh: incl}, nil
}

func (p *parser) parseRangeToken() (string, error) {
	start := p.pos
	for {
		r := p.peek()
		if r == eof || unicode.IsSpace(r) || strings.ToUpper(p.rest()) == "TO" || r == ']' || r == '}' {
			break
		}
		p.next()
	}
	if start == p.pos {
		return "", fmt.Errorf("expected range bound at %d", p.pos)
	}
	return p.input[start:p.pos], nil
}

func (p *parser) parseGroup() (Node, error) {
	p.next() // consume (
	q, err := p.parseBoolean()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.next() != ')' {
		return nil, fmt.Errorf("expected ')' at %d", p.pos)
	}
	return Group{Query: q}, nil
}

func (p *parser) parseWord() (Node, error) {
	start := p.pos
	escaped := false
	for {
		r := p.peek()
		if r == eof {
			break
		}
		if escaped {
			escaped = false
			p.next()
			continue
		}
		if r == '\\' {
			escaped = true
			p.next()
			continue
		}
		if unicode.IsSpace(r) || isSpecial(r) {
			break
		}
		p.next()
	}
	if start == p.pos {
		return nil, fmt.Errorf("expected term at %d", p.pos)
	}
	value := p.input[start:p.pos]
	wc := strings.ContainsAny(value, "*?")
	prefix := strings.HasSuffix(value, "*") && !strings.ContainsAny(strings.TrimSuffix(value, "*"), "*?")
	return Term{Value: value, Wildcard: wc, Prefix: prefix}, nil
}

// isSpecial returns true for runes that terminate a bare word outside of quotes.
func isSpecial(r rune) bool {
	switch r {
	case '(', ')', '[', ']', '{', '}', '"', ':', '+', '-':
		return true
	}
	return false
}
