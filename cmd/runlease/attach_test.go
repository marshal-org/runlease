package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMarkerWriter_singleChunk(t *testing.T) {
	var out bytes.Buffer
	w := &markerWriter{out: &out, marker: []byte("__DONE__")}
	if _, err := w.Write([]byte("hello __DONE__ world")); err != nil {
		t.Fatal(err)
	}
	if !w.matched {
		t.Error("expected matched=true after single-chunk write containing marker")
	}
	if !strings.Contains(out.String(), "world") {
		t.Errorf("output should still contain trailing bytes, got %q", out.String())
	}
}

func TestMarkerWriter_splitAcrossChunks(t *testing.T) {
	var out bytes.Buffer
	w := &markerWriter{out: &out, marker: []byte("__DONE__")}
	chunks := []string{"hello __D", "ONE__ tail"}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if !w.matched {
		t.Error("expected matched=true when marker is split across writes")
	}
}

func TestMarkerWriter_noMatch(t *testing.T) {
	var out bytes.Buffer
	w := &markerWriter{out: &out, marker: []byte("__DONE__")}
	if _, err := w.Write([]byte("nothing to see")); err != nil {
		t.Fatal(err)
	}
	if w.matched {
		t.Error("matched should be false when marker not present")
	}
}
