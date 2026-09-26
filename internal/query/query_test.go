package query

import (
	"reflect"
	"testing"
)

func TestParseTermAndField(t *testing.T) {
	n, err := Parse("title:hello")
	if err != nil {
		t.Fatal(err)
	}
	tn, ok := n.(*TermNode)
	if !ok || tn.Term != "hello" || tn.Field != "title" || tn.Fuzzy != nil {
		t.Fatalf("unexpected: %#v", n)
	}
}

func TestParseFuzzyMarkers(t *testing.T) {
	n, _ := Parse("hello~")
	tn := n.(*TermNode)
	if tn.Fuzzy == nil || *tn.Fuzzy != true {
		t.Fatal("~ should force fuzzy")
	}
	n, _ = Parse("hello$")
	tn = n.(*TermNode)
	if tn.Fuzzy == nil || *tn.Fuzzy != false {
		t.Fatal("$ should force exact")
	}
}

func TestParsePhrase(t *testing.T) {
	n, err := Parse(`body:"new york"`)
	if err != nil {
		t.Fatal(err)
	}
	pn, ok := n.(*PhraseNode)
	if !ok || !reflect.DeepEqual(pn.Terms, []string{"new", "york"}) || pn.Field != "body" {
		t.Fatalf("unexpected: %#v", n)
	}
}

func TestParseBoolean(t *testing.T) {
	n, err := Parse("hello AND world AND NOT foo")
	if err != nil {
		t.Fatal(err)
	}
	and, ok := n.(*AndNode)
	if !ok || len(and.Children) != 3 {
		t.Fatalf("want AndNode with 3 children, got %#v", n)
	}
	if _, ok := and.Children[2].(*NotNode); !ok {
		t.Fatalf("third child must be NotNode: %#v", and.Children[2])
	}
}

func TestImplicitNotIsOr(t *testing.T) {
	n, err := Parse("hello AND world NOT foo")
	if err != nil {
		t.Fatal(err)
	}
	or, ok := n.(*OrNode)
	if !ok || len(or.Children) != 2 {
		t.Fatalf("want OrNode 2 children, got %#v", n)
	}
	if _, ok := or.Children[0].(*AndNode); !ok {
		t.Fatalf("first child must be AndNode: %#v", or.Children[0])
	}
	if _, ok := or.Children[1].(*NotNode); !ok {
		t.Fatalf("second child must be NotNode: %#v", or.Children[1])
	}
}

func TestParseOrDefaultAndParens(t *testing.T) {
	n, err := Parse("a (b AND c) -d")
	if err != nil {
		t.Fatal(err)
	}
	or, ok := n.(*OrNode)
	if !ok || len(or.Children) != 3 {
		t.Fatalf("want OrNode 3 children, got %#v", n)
	}
	if _, ok := or.Children[1].(*AndNode); !ok {
		t.Fatal("middle must be AND")
	}
	if _, ok := or.Children[2].(*NotNode); !ok {
		t.Fatal("last must be NOT from - prefix")
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"",
		`"unclosed`,
		"field:",
		"a AND",
		"NOT",
		"( a",
		"a +",
	}
	for _, q := range bad {
		if _, err := Parse(q); err == nil {
			t.Errorf("query %q should fail", q)
		}
	}
}
