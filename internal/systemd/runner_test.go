package systemd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerLiteralAndError(t *testing.T) {
	r := ExecRunner{}
	out, err := r.Output(context.Background(), "printf", "%s", "a b;$HOME%")
	if err != nil || string(out) != "a b;$HOME%" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err = r.Output(context.Background(), "sh", "-c", "echo failure >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("%v", err)
	}
	var buffer bytes.Buffer
	if err = r.Stream(context.Background(), &buffer, "printf", "%s", "hello"); err != nil || buffer.String() != "hello" {
		t.Fatal(err, buffer.String())
	}
	if err = r.Stream(context.Background(), &buffer, "sh", "-c", "exit 1"); err == nil {
		t.Fatal("lost exit status")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Output(ctx, "sleep", "30"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestWait(t *testing.T) {
	if err := Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, time.Hour); err != context.Canceled {
		t.Fatal(err)
	}
}
