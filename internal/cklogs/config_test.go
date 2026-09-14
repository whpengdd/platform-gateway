package cklogs

import "testing"

func TestFileValuesAreNotTrimmed(t *testing.T) {
	c := &Client{User: " ", Pass: " ", Index: " explicit-index "}
	if !c.Configured() || c.index() != c.Index {
		t.Fatal("explicit configuration changed")
	}
}
