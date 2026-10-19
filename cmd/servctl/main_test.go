package main

import (
	"os"
	"testing"
)

func TestVersionEntrypoint(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"servctl", "--version"}
	if code := run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
