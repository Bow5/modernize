package main

import (
	"strings"
	"testing"
)

func TestVerifyBowParser(t *testing.T) {
	if err := verifyBowParser(); err != nil {
		t.Fatal(err)
	}
}

func TestBowEnvSetsGOROOT(t *testing.T) {
	env := bowEnv()
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "GOROOT=") {
			found = true
			if strings.TrimPrefix(e, "GOROOT=") != goRoot() {
				t.Fatalf("GOROOT=%q want %q", e, goRoot())
			}
		}
	}
	if !found {
		t.Fatal("GOROOT missing from bowEnv")
	}
}
