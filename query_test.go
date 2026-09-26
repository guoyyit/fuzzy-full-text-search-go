package fuzzsearch

import "testing"

func TestParseTermFuzzySuffix(t *testing.T) {
	cases := []struct {
		in    string
		term  string
		maxEd int
	}{
		{"cat", "cat", -1},
		{"cat~", "cat", -2},
		{"cat~0", "cat", 0},
		{"cat~3", "cat", 3},
		{"title:cat~2", "cat", 2},
	}
	for _, c := range cases {
		q, err := parseQuery(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		tq, ok := q.(*TermQuery)
		if !ok {
			t.Errorf("%q: got %T, want TermQuery", c.in, q)
			continue
		}
		if tq.Term != c.term || tq.MaxEd != c.maxEd {
			t.Errorf("%q: term=%q maxEd=%d, want %q %d", c.in, tq.Term, tq.MaxEd, c.term, c.maxEd)
		}
		if c.in == "title:cat~2" && tq.Field != "title" {
			t.Errorf("field = %q", tq.Field)
		}
	}
}

func TestParseAST(t *testing.T) {
	if q, err := parseQuery("a AND b"); err != nil || len(q.(*AndQuery).Children) != 2 {
		t.Errorf("AND parse: %#v err=%v", q, err)
	}
	if q, err := parseQuery("a b"); err != nil || len(q.(*OrQuery).Children) != 2 {
		t.Errorf("implicit OR parse: %#v err=%v", q, err)
	}
	if q, err := parseQuery("a OR b"); err != nil || len(q.(*OrQuery).Children) != 2 {
		t.Errorf("OR parse: %#v err=%v", q, err)
	}
	if q, err := parseQuery("NOT a"); err != nil {
		t.Errorf("NOT parse: %v", err)
	} else if _, ok := q.(*NotQuery); !ok {
		t.Errorf("NOT parse: got %T", q)
	}
	if q, err := parseQuery(`f:"a b"`); err != nil {
		t.Errorf("field phrase: %v", err)
	} else {
		pq := q.(*PhraseQuery)
		if pq.Field != "f" || len(pq.Terms) != 2 || pq.Fuzzy {
			t.Errorf("field phrase: %+v", pq)
		}
	}
	if q, err := parseQuery(`"a b"~2`); err != nil {
		t.Errorf("fuzzy phrase: %v", err)
	} else {
		pq := q.(*PhraseQuery)
		if !pq.Fuzzy || pq.MaxEd != 2 {
			t.Errorf("fuzzy phrase: %+v", pq)
		}
	}
}
