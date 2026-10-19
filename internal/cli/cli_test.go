package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/9star-academy/servctl/internal/systemd"
)

type fakeBackend struct {
	config       systemd.Config
	action, name string
	follow       bool
	err          error
}

func (b *fakeBackend) Install(_ context.Context, c systemd.Config) error { b.config = c; return b.err }
func (b *fakeBackend) Action(_ context.Context, a, n string, f bool) error {
	b.action = a
	b.name = n
	b.follow = f
	return b.err
}
func testApp(t *testing.T) (*App, *fakeBackend, *bytes.Buffer) {
	t.Helper()
	b := &fakeBackend{}
	out := &bytes.Buffer{}
	return &App{Backend: b, Out: out, Err: out, Version: "v0.1.0", GOOS: "linux", EUID: 0, Cwd: t.TempDir(), CheckSystem: func() error { return nil }}, b, out
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
				a.CheckSystem = func() error { return errors.New("not booted") }
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
		if b.action != action || b.name != "admin" || b.follow != (action == "logs") {
			t.Fatal(b)
		}
		b.err = errors.New("failed")
		if a.Run(context.Background(), args) == 0 {
			t.Fatal("lost error")
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
