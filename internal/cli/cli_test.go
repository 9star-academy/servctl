package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/9star-academy/servctl/internal/systemd"
)

type fakeBackend struct {
	config       systemd.Config
	action, name string
	logOpts      systemd.LogOptions
	listOpts     systemd.ListOptions
	readyOpts    systemd.ReadyCheck
	listed       bool
	err          error
}

func (b *fakeBackend) Install(_ context.Context, c systemd.Config) error { b.config = c; return b.err }
func (b *fakeBackend) List(_ context.Context, opts systemd.ListOptions) error {
	b.listed = true
	b.listOpts = opts
	return b.err
}
func (b *fakeBackend) Action(_ context.Context, a, n string, ready systemd.ReadyCheck) error {
	b.action = a
	b.name = n
	b.readyOpts = ready
	return b.err
}
func (b *fakeBackend) Logs(_ context.Context, n string, opts systemd.LogOptions) error {
	b.action = "logs"
	b.name = n
	b.logOpts = opts
	return b.err
}
func testApp(t *testing.T) (*App, *fakeBackend, *bytes.Buffer) {
	t.Helper()
	b := &fakeBackend{}
	out := &bytes.Buffer{}
	return &App{Backend: b, Out: out, Err: out, Version: "v0.1.0", GOOS: "linux", EUID: 0, Cwd: t.TempDir(), CheckSystem: func(string) error { return nil }}, b, out
}

func TestInstallParsesNameFirstAndLiteralArgs(t *testing.T) {
	a, b, out := testApp(t)
	code := a.Run(context.Background(), []string{"install", "admin", "--exec", "./admin", "--user", "ubuntu", "--", "--port", "8200", "a b", "$HOME"})
	if code != 0 {
		t.Fatal(out.String())
	}
	if b.config.Name != "admin" || b.config.Executable != filepath.Join(a.Cwd, "admin") || b.config.WorkDir != a.Cwd || b.config.User != "ubuntu" || strings.Join(b.config.Args, "|") != "--port|8200|a b|$HOME" {
		t.Fatalf("%+v", b.config)
	}
}

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"oops"}, {"install"}, {"install", "admin"}, {"install", "admin", "--exec", "./app"}, {"install", "admin", "--exec", "./app", "--user", "ubuntu", "extra"}, {"stop"}, {"stop", "admin", "extra"}, {"logs", "admin", "--bad"}, {"install", "../bad", "--exec", "./app", "--user", "ubuntu"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, b, _ := testApp(t)
			if a.Run(context.Background(), args) == 0 {
				t.Fatal("accepted invalid arguments")
			}
			if b.action != "" || b.config.Name != "" {
				t.Fatal("called backend")
			}
		})
	}
}

func TestHelpAndVersionOutsideLinux(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"--version"}, {"install", "--help"}} {
		a, _, out := testApp(t)
		a.GOOS = "darwin"
		if a.Run(context.Background(), args) != 0 {
			t.Fatal(out.String())
		}
		if out.Len() == 0 {
			t.Fatal("empty output")
		}
	}
}

func TestRuntimeGuards(t *testing.T) {
	for _, kind := range []string{"platform", "root", "system"} {
		t.Run(kind, func(t *testing.T) {
			a, b, _ := testApp(t)
			switch kind {
			case "platform":
				a.GOOS = "darwin"
			case "root":
				a.EUID = 1000
			case "system":
				a.CheckSystem = func(string) error { return errors.New("not booted") }
			}
			if a.Run(context.Background(), []string{"stop", "admin"}) == 0 {
				t.Fatal("accepted")
			}
			if b.action != "" {
				t.Fatal("called backend")
			}
		})
	}
}

func TestActionsAndBackendErrors(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "status", "logs", "uninstall"} {
		a, b, out := testApp(t)
		args := []string{action, "admin"}
		if action == "logs" {
			args = append(args, "--follow")
		}
		if a.Run(context.Background(), args) != 0 {
			t.Fatal(out.String())
		}
		if b.action != action || b.name != "admin" || b.logOpts.Follow != (action == "logs") {
			t.Fatal(b)
		}
		b.err = errors.New("failed")
		if a.Run(context.Background(), args) == 0 {
			t.Fatal("lost error")
		}
	}
}

func TestSystemServiceNames(t *testing.T) {
	for _, name := range []string{"nginx.service", "getty@tty1", "getty@tty1.service", "worker@a\\x2db.service"} {
		a, b, out := testApp(t)
		a.EUID = 1000
		if code := a.Run(context.Background(), []string{"status", name}); code != 0 || b.name != name {
			t.Fatalf("status %s: exit %d, backend = %+v, output = %s", name, code, b, out)
		}
	}
	for _, args := range [][]string{
		{"restart", "worker*.service"},
		{"start", "/usr/lib/systemd/system/worker.service"},
		{"logs", "worker@.service"},
		{"stop", "worker\nother"},
		{"install", "worker.service", "--exec", "./app", "--user", "ubuntu"},
		{"uninstall", "worker.service"},
		{"uninstall", "worker@backup"},
	} {
		a, b, _ := testApp(t)
		if a.Run(context.Background(), args) == 0 || b.action != "" || b.config.Name != "" {
			t.Fatalf("accepted invalid arguments %q", args)
		}
	}
}

func TestNewAndCancellation(t *testing.T) {
	var out bytes.Buffer
	a, err := New("test", &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if a.Run(context.Background(), []string{"--version"}) != 0 || !strings.Contains(out.String(), "test") {
		t.Fatal(out.String())
	}
	a, b, _ := testApp(t)
	b.err = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a.Run(ctx, []string{"stop", "admin"}) != 130 {
		t.Fatal("wrong interrupted status")
	}
}

func TestInstallWorkdirAndBackendFailure(t *testing.T) {
	a, b, _ := testApp(t)
	b.err = errors.New("startup failed")
	if a.Run(context.Background(), []string{"install", "admin", "--exec", "./admin", "--workdir", "relative", "--user", "ubuntu"}) == 0 {
		t.Fatal("lost startup error")
	}
	if b.config.WorkDir != filepath.Join(a.Cwd, "relative") {
		t.Fatal(b.config.WorkDir)
	}
	a, _, _ = testApp(t)
	if a.Run(context.Background(), []string{"install", "admin", "--help"}) != 0 {
		t.Fatal("help failed")
	}
	if a.Run(context.Background(), []string{"install", "admin", "--exec", "./app", "--user", "ubuntu", "--workdir", ""}) == 0 {
		t.Fatal("empty workdir")
	}
}

func TestListIsReadOnlyAndNeedsNoName(t *testing.T) {
	a, b, out := testApp(t)
	a.EUID = 1000
	if code := a.Run(context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !b.listed || b.action != "" || b.config.Name != "" {
		t.Fatalf("unexpected backend call: %+v", b)
	}
}

func TestListRejectsArguments(t *testing.T) {
	for _, args := range [][]string{{"list", "admin"}, {"list", "--follow"}, {"list", "--unknown"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, b, _ := testApp(t)
			if a.Run(context.Background(), args) == 0 {
				t.Fatal("accepted list arguments")
			}
			if b.listed || b.action != "" || b.config.Name != "" {
				t.Fatal("called backend for invalid arguments")
			}
		})
	}
}

func TestListRuntimeGuardsAndErrors(t *testing.T) {
	for _, kind := range []string{"platform", "system", "backend", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			a, b, out := testApp(t)
			ctx := context.Background()
			wantCode := 1
			switch kind {
			case "platform":
				a.GOOS = "darwin"
			case "system":
				a.CheckSystem = func(string) error { return errors.New("not booted") }
			case "backend":
				b.err = errors.New("list failed")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				b.err = context.Canceled
				wantCode = 130
			}
			if code := a.Run(ctx, []string{"list"}); code != wantCode {
				t.Fatalf("exit %d, want %d: %s", code, wantCode, out.String())
			}
			if b.listed != (kind == "backend" || kind == "cancelled") {
				t.Fatalf("unexpected list dispatch: %+v", b)
			}
		})
	}
}

func TestListHelpOutsideLinux(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"list", "--help"}, {"list", "-h"}} {
		a, b, out := testApp(t)
		a.GOOS = "darwin"
		if a.Run(context.Background(), args) != 0 || !strings.Contains(out.String(), "servctl list") {
			t.Fatal(out.String())
		}
		if b.listed {
			t.Fatal("help called backend")
		}
	}
}

func TestUpdateCommand(t *testing.T) {
	for _, tt := range []struct {
		name, platform string
		args           []string
		updateErr      error
		wantCode       int
		wantCalled     bool
		wantOutput     string
	}{
		{"linux without systemd or root", "linux", []string{"update"}, nil, 0, true, "v0.1.0"},
		{"help on macOS", "darwin", []string{"update", "--help"}, nil, 0, false, "servctl update"},
		{"macOS", "darwin", []string{"update"}, nil, 1, false, "仅支持 Linux"},
		{"extra argument", "linux", []string{"update", "myservice"}, nil, 1, false, "update 不接受"},
		{"permission denied", "linux", []string{"update"}, os.ErrPermission, 1, true, "sudo servctl update"},
		{"network error", "linux", []string{"update"}, errors.New("download failed"), 1, true, "download failed"},
		{"cancelled", "linux", []string{"update"}, context.Canceled, 130, true, "context canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, b, out := testApp(t)
			a.GOOS, a.EUID = tt.platform, 1000
			a.CheckSystem = func(string) error {
				t.Fatal("update queried systemd")
				return nil
			}
			called := false
			a.update = func(_ context.Context, current string, w io.Writer) error {
				called = true
				_, _ = io.WriteString(w, current+"\n")
				return tt.updateErr
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if errors.Is(tt.updateErr, context.Canceled) {
				cancel()
			}
			if code := a.Run(ctx, tt.args); code != tt.wantCode || called != tt.wantCalled || !strings.Contains(out.String(), tt.wantOutput) {
				t.Fatalf("exit = %d, update called = %t, output = %q", code, called, out.String())
			}
			if b.listed || b.action != "" || b.config.Name != "" {
				t.Fatalf("update called service backend: %+v", b)
			}
		})
	}
}

func TestStatusPreservesExitCodeWithoutNoise(t *testing.T) {
	a, b, out := testApp(t)
	err := exec.Command("sh", "-c", "exit 3").Run()
	if err == nil {
		t.Fatal("expected exit 3")
	}
	b.err = fmt.Errorf("systemctl: %w", err)
	if code := a.Run(context.Background(), []string{"status", "admin"}); code != 3 {
		t.Fatalf("exit = %d, want 3 (out=%q)", code, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected error noise on status exit 3: %q", out.String())
	}
}

func TestLogsFollowCtrlCExitsSilently(t *testing.T) {
	a, b, out := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.err = context.Canceled
	if code := a.Run(ctx, []string{"logs", "admin", "--follow"}); code != 130 {
		t.Fatalf("exit = %d, want 130", code)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected error noise on logs --follow cancel: %q", out.String())
	}
}

func TestInstallOptionalFlags(t *testing.T) {
	a, b, out := testApp(t)
	code := a.Run(context.Background(), []string{
		"install", "admin",
		"--exec", "./admin",
		"--user", "ubuntu",
		"--env", "A=1",
		"--env", "B=2",
		"--env-file", "./.env",
		"--desc", "Admin API",
		"--restart", "always",
		"--restart-sec", "5s",
		"--memory-max", "512M",
		"--cpu-quota", "150%",
		"--after", "postgresql.service",
		"--after", "redis",
		"--no-enable",
		"--no-start",
	})
	if code != 0 {
		t.Fatal(out.String())
	}
	c := b.config
	if strings.Join(c.Env, ",") != "A=1,B=2" ||
		len(c.EnvFiles) != 1 || c.EnvFiles[0] != filepath.Join(a.Cwd, ".env") ||
		c.Description != "Admin API" || c.Restart != "always" || c.RestartSec != "5s" ||
		c.MemoryMax != "512M" || c.CPUQuota != "150%" ||
		strings.Join(c.After, ",") != "postgresql.service,redis" ||
		!c.NoEnable || !c.NoStart {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLogsOptionsFlags(t *testing.T) {
	a, b, out := testApp(t)
	if code := a.Run(context.Background(), []string{"logs", "admin", "-f", "-n", "120", "--since", "1 hour ago", "--level", "err"}); code != 0 {
		t.Fatal(out.String())
	}
	if !b.logOpts.Follow || b.logOpts.Lines != 120 || b.logOpts.Since != "1 hour ago" || b.logOpts.Level != "err" {
		t.Fatalf("unexpected log opts: %+v", b.logOpts)
	}
	for _, bad := range [][]string{
		{"logs", "admin", "--lines", "0"},
		{"logs", "admin", "-n", "-5"},
		{"logs", "admin", "--level", "verbose"},
	} {
		a, b, _ := testApp(t)
		if a.Run(context.Background(), bad) == 0 || b.action != "" {
			t.Fatalf("accepted invalid logs args %q", bad)
		}
	}
}

func TestShowEnableDisableListAndReadyFlags(t *testing.T) {
	// show is read-only (no root required).
	a, b, out := testApp(t)
	a.EUID = 1000
	if code := a.Run(context.Background(), []string{"show", "nginx.service"}); code != 0 || b.action != "show" || b.name != "nginx.service" {
		t.Fatalf("show failed: code=%d b=%+v out=%s", code, b, out.String())
	}

	// enable / disable require root and work with root.
	for _, act := range []string{"enable", "disable"} {
		a, b, _ = testApp(t)
		a.EUID = 1000
		if a.Run(context.Background(), []string{act, "admin"}) == 0 || b.action != "" {
			t.Fatalf("%s accepted non-root", act)
		}
		a.EUID = 0
		if a.Run(context.Background(), []string{act, "admin"}) != 0 || b.action != act {
			t.Fatalf("%s failed with root", act)
		}
	}

	// list --json --all --failed
	a, b, out = testApp(t)
	a.EUID = 1000
	if code := a.Run(context.Background(), []string{"list", "--json", "--all", "--failed"}); code != 0 {
		t.Fatal(out.String())
	}
	if !b.listed || !b.listOpts.JSON || !b.listOpts.All || !b.listOpts.Failed {
		t.Fatalf("unexpected listOpts: %+v", b.listOpts)
	}

	// install --replace + ready checks
	a, b, out = testApp(t)
	if code := a.Run(context.Background(), []string{
		"install", "admin", "--replace", "--exec", "./app", "--user", "ubuntu",
		"--wait-port", "8200", "--health-url", "http://127.0.0.1:8200/health", "--ready-timeout", "30s",
	}); code != 0 {
		t.Fatal(out.String())
	}
	if !b.config.Replace || b.config.Ready.WaitPort != 8200 || b.config.Ready.HealthURL != "http://127.0.0.1:8200/health" || b.config.Ready.Timeout != 30*time.Second {
		t.Fatalf("unexpected replace/ready config: %+v", b.config)
	}

	// restart with ready check
	a, b, out = testApp(t)
	if code := a.Run(context.Background(), []string{"restart", "admin", "--wait-port", "8200", "--health-url", "http://127.0.0.1:8200/health"}); code != 0 {
		t.Fatal(out.String())
	}
	if b.action != "restart" || b.readyOpts.WaitPort != 8200 || b.readyOpts.HealthURL != "http://127.0.0.1:8200/health" {
		t.Fatalf("unexpected restart ready opts: %+v", b.readyOpts)
	}
}
