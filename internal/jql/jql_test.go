package jql

import (
	"strings"
	"testing"
)

func TestBoundaries(t *testing.T) {
	for _, s := range []string{`labels = zammad OR labels = support`, `assignee = currentUser()`, `(labels = "a\\b")`, `summary ~ "ORDER BY"`} {
		if err := Validate(s); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []string{`labels=x) OR (project=IT`, `labels=x ORDER BY key`, `labels="x`, `(labels=x`, `labels=x;project=IT`, `labels=x /*foo*/`} {
		if Validate(s) == nil {
			t.Fatal("accepted", s)
		}
	}
	q := Scope("CS", `labels=x OR labels=y`, `text ~ `+Literal(`") OR project = IT`))
	if !strings.HasPrefix(q, `(project = "CS") AND (labels=x OR labels=y) AND (`) || !strings.Contains(q, `\"`) {
		t.Fatal(q)
	}
}
func TestFiniteProof(t *testing.T) {
	for _, s := range []string{`labels = zammad`, `(( labels = "zammad" ))`, `labels = 'zammad'`} {
		v, ok := Label(s)
		if !ok || v != "zammad" {
			t.Fatal(s)
		}
	}
	for _, s := range []string{`labels=x OR labels=y`, `labels=x AND labels=y`, `labels=foo()`, `cf[1]=x`, `labels WAS x`, `labels=EMPTY`, `labels=x) OR (labels=y`, `labels = "x" trailing`, `labels == x`} {
		if _, ok := Label(s); ok {
			t.Fatal("accepted", s)
		}
	}
}
