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
	var stdout, stderr bytes.Buffer
	if err = r.Stream(context.Background(), &stdout, &stderr, "sh", "-c", "printf out; printf err >&2"); err != nil || stdout.String() != "out" || stderr.String() != "err" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if err = r.Stream(context.Background(), &stdout, &stderr, "sh", "-c", "exit 1"); err == nil {
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
