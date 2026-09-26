package fuzzsearch

import (
	"fmt"
	"strconv"
	"strings"
)

// Query is the abstract syntax tree of a search expression.
type Query interface{ isQuery() }

// TermQuery matches a single term, optionally with fuzzy matching
// ("term~", "term~2"). An empty Field means every configured field.
type TermQuery struct {
	Field string
	Term  string
	// MaxEd is the per-term edit-distance cap; -1 means "not specified",
	// in which case Config.DefaultMaxEditDistance applies.
	MaxEd int
}

// PhraseQuery matches consecutive terms ("hello world"). With Fuzzy
// set ("a b"~N) each position may match a term within MaxEd edits.
type PhraseQuery struct {
	Field string
	Terms []string
	Fuzzy bool
	MaxEd int
}

// AndQuery requires all children to match (AND / &&).
type AndQuery struct{ Children []Query }

// OrQuery requires at least one child to match (OR / ||, or implicit
// adjacency: "a b" means "a OR b").
type OrQuery struct{ Children []Query }

// NotQuery excludes documents matching the child (NOT / ! / -).
type NotQuery struct{ Child Query }

func (*TermQuery) isQuery()   {}
func (*PhraseQuery) isQuery() {}
func (*AndQuery) isQuery()    {}
func (*OrQuery) isQuery()     {}
func (*NotQuery) isQuery()    {}

// --------------------------------------------------------------------
// Lexer
// --------------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tWord
	tAnd
	tOr
	tNot
	tLParen
	tRParen
	tQuote
)

type qtoken struct {
	kind tokKind
	text string
}

func lexQuery(input string) ([]qtoken, error) {
	var toks []qtoken
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, qtoken{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, qtoken{tRParen, ")"})
			i++
		case c == '"':
			toks = append(toks, qtoken{tQuote, `"`})
			i++
		case c == '&' && i+1 < len(input) && input[i+1] == '&':
			toks = append(toks, qtoken{tAnd, "&&"})
			i += 2
		case c == '|' && i+1 < len(input) && input[i+1] == '|':
			toks = append(toks, qtoken{tOr, "||"})
			i += 2
		case c == '!' || c == '-':
			toks = append(toks, qtoken{tNot, string(c)})
			i++
		default:
			start := i
			for i < len(input) && !strings.ContainsRune(" \t\n\r()\"!-", rune(input[i])) {
				i++
			}
			w := input[start:i]
			switch w {
			case "AND":
				toks = append(toks, qtoken{tAnd, w})
			case "OR":
				toks = append(toks, qtoken{tOr, w})
			case "NOT":
				toks = append(toks, qtoken{tNot, w})
			default:
				toks = append(toks, qtoken{tWord, w})
			}
		}
	}
	return toks, nil
}

// --------------------------------------------------------------------
// Parser
//
//	query   := orExpr
//	orExpr  := andExpr ( ("OR"|"||") andExpr | andExpr )*   // adjacency = OR
//	andExpr := unary ( ("AND"|"&&") unary )*
//	unary   := ("NOT"|"!"|"-") unary | primary
//	primary := "(" orExpr ")" | field? phrase | field? term
//	phrase  := '"' word+ '"' ("~" num?)?
//	term    := word ("~" num?)?
//	field   := word ":"            (applies to the following primary)
// --------------------------------------------------------------------

type parser struct {
	toks []qtoken
	pos  int
}

func parseQuery(input string) (Query, error) {
	toks, err := lexQuery(input)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("query: empty query")
	}
	p := &parser{toks: toks}
	q, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("query: unexpected trailing %q", p.toks[p.pos].text)
	}
	return q, nil
}

func (p *parser) peek() qtoken {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return qtoken{tEOF, ""}
}

func (p *parser) next() qtoken {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

func (p *parser) parseOr() (Query, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []Query{left}
	for {
		t := p.peek()
		if t.kind == tOr {
			p.next()
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			children = append(children, right)
		} else if t.kind == tWord || t.kind == tNot || t.kind == tLParen || t.kind == tQuote {
			// Implicit OR between adjacent expressions.
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			children = append(children, right)
		} else {
			break
		}
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &OrQuery{Children: children}, nil
}

func (p *parser) parseAnd() (Query, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	children := []Query{left}
	for p.peek().kind == tAnd {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &AndQuery{Children: children}, nil
}

func (p *parser) parseUnary() (Query, error) {
	if p.peek().kind == tNot {
		p.next()
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &NotQuery{Child: child}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Query, error) {
	t := p.peek()
	switch t.kind {
	case tLParen:
		p.next()
		q, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, fmt.Errorf("query: missing closing parenthesis")
		}
		p.next()
		return q, nil
	case tQuote:
		return p.parsePhrase("")
	case tWord:
		return p.parseWord()
	case tEOF:
		return nil, fmt.Errorf("query: unexpected end of input")
	default:
		return nil, fmt.Errorf("query: unexpected %q", t.text)
	}
}

// parseWord handles a WORD token that may be "term", "term~N",
// "field:term", "field:" followed by phrase/paren, or "field:" alone
// (an error).
func (p *parser) parseWord() (Query, error) {
	w := p.next().text

	// Split optional field qualifier: first ':' that is not at position 0.
	field := ""
	rest := w
	if idx := strings.Index(w, ":"); idx >= 0 {
		field = w[:idx]
		rest = w[idx+1:]
		if field == "" {
			return nil, fmt.Errorf("query: empty field name in %q", w)
		}
		if rest == "" {
			// "field:" followed by phrase or group.
			switch p.peek().kind {
			case tQuote:
				return p.parsePhrase(field)
			case tLParen:
				p.next()
				q, err := p.parseOr()
				if err != nil {
					return nil, err
				}
				if p.peek().kind != tRParen {
					return nil, fmt.Errorf("query: missing closing parenthesis after field group")
				}
				p.next()
				return qualify(q, field)
			default:
				return nil, fmt.Errorf("query: field %q must be followed by a term, phrase or group", field)
			}
		}
	}

	term, maxEd, err := splitFuzzy(rest)
	if err != nil {
		return nil, err
	}
	if term == "" {
		return nil, fmt.Errorf("query: empty term in %q", w)
	}
	return &TermQuery{Field: field, Term: strings.ToLower(term), MaxEd: maxEd}, nil
}

// qualify rewrites every unqualified leaf of q to use the given field.
func qualify(q Query, field string) (Query, error) {
	switch n := q.(type) {
	case *TermQuery:
		if n.Field == "" {
			n.Field = field
		}
		return n, nil
	case *PhraseQuery:
		if n.Field == "" {
			n.Field = field
		}
		return n, nil
	case *AndQuery:
		for i, c := range n.Children {
			nc, err := qualify(c, field)
			if err != nil {
				return nil, err
			}
			n.Children[i] = nc
		}
		return n, nil
	case *OrQuery:
		for i, c := range n.Children {
			nc, err := qualify(c, field)
			if err != nil {
				return nil, err
			}
			n.Children[i] = nc
		}
		return n, nil
	case *NotQuery:
		nc, err := qualify(n.Child, field)
		if err != nil {
			return nil, err
		}
		n.Child = nc
		return n, nil
	}
	return q, nil
}

// splitFuzzy splits "term~2" into ("term", 2). Returns maxEd=-1 when no
// '~' is present, and maxEd=-2 for a bare '~' (meaning: use default).
func splitFuzzy(s string) (term string, maxEd int, err error) {
	idx := strings.Index(s, "~")
	if idx < 0 {
		return s, -1, nil
	}
	term = s[:idx]
	suffix := s[idx+1:]
	if suffix == "" {
		return term, -2, nil
	}
	n, convErr := strconv.Atoi(suffix)
	if convErr != nil || n < 0 {
		return "", 0, fmt.Errorf("query: invalid fuzzy suffix %q (want ~ or ~N with N >= 0)", s)
	}
	return term, n, nil
}

func (p *parser) parsePhrase(field string) (Query, error) {
	p.next() // consume opening quote
	var terms []string
	for {
		t := p.next()
		switch t.kind {
		case tQuote:
			if len(terms) == 0 {
				return nil, fmt.Errorf("query: empty phrase")
			}
			// Optional fuzzy suffix: ~ or ~N directly after the quote.
			fuzzy := false
			maxEd := -1
			if p.peek().kind == tWord && strings.HasPrefix(p.peek().text, "~") {
				sfx := p.next().text
				_, ed, err := splitFuzzy("x" + sfx)
				if err != nil {
					return nil, err
				}
				fuzzy = true
				maxEd = ed
			}
			lowered := make([]string, len(terms))
			for i, t := range terms {
				lowered[i] = strings.ToLower(t)
			}
			return &PhraseQuery{Field: field, Terms: lowered, Fuzzy: fuzzy, MaxEd: maxEd}, nil
		case tWord:
			terms = append(terms, t.text)
		case tEOF:
			return nil, fmt.Errorf("query: unterminated phrase")
		default:
			return nil, fmt.Errorf("query: unexpected %q inside phrase", t.text)
		}
	}
}

// evalQuery is implemented in eval.go.
