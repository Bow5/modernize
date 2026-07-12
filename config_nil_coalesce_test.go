package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestNilCoalesceFallbackDefaultFalse(t *testing.T) {
	if DefaultConfig().NilCoalesceFallback {
		t.Fatal("expected default false")
	}
}

func TestPrintNilCoalesceFallbackNoticeWhenDisabled(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	printNilCoalesceFallbackNotice(false)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "nil_coalesce_fallback: false") {
		t.Fatalf("missing disabled notice: %q", out)
	}
	if !strings.Contains(out, "??") {
		t.Fatalf("missing example syntax: %q", out)
	}
	if !strings.Contains(out, "modernize.json") {
		t.Fatalf("missing config hint: %q", out)
	}
}

func TestPrintNilCoalesceFallbackNoticeWhenEnabled(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	printNilCoalesceFallbackNotice(true)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	if buf.Len() != 0 {
		t.Fatalf("expected no output when enabled, got %q", buf.String())
	}
}
