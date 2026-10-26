package systemd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls            []string
	fail             string
	existing         bool
	inactive         bool
	previousRestarts bool
	states           map[string]string
}

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	s := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, s)
	if r.fail != "" && strings.Contains(s, r.fail) {
		return []byte("simulated failure"), errors.New("exit 1")
	}
	if strings.Contains(s, "--property=UnitFileState") {
		return []byte(r.states[args[len(args)-1]]), nil
	}
	if strings.Contains(s, "--property=LoadState") {
		if r.existing {
			return []byte("loaded\n"), nil
		}
		return []byte("not-found\n"), nil
	}
	if strings.Contains(s, "--property=ActiveState") {
		if r.inactive {
			return []byte("ActiveState=failed\nNRestarts=1\n"), nil
		}
		if r.previousRestarts {
			return []byte("ActiveState=active\nNRestarts=2\n"), nil
		}
		return []byte("ActiveState=active\nNRestarts=0\n"), nil
	}
	return nil, nil
}

func TestStartAcceptsActiveServiceWithHistoricalRestarts(t *testing.T) {
	m, r := testManager(t)
	if err := os.WriteFile(filepath.Join(m.UnitDir, "admin.service"), []byte(Marker), 0644); err != nil {
		t.Fatal(err)
	}
	r.previousRestarts = true
	if err := m.Action(context.Background(), "start", "admin", false); err != nil {
		t.Fatal(err)
	}
}
func (r *fakeRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	out, err := r.Output(ctx, name, args...)
	_, _ = w.Write(out)
	return err
}
func testManager(t *testing.T) (*Manager, *fakeRunner) {
	t.Helper()
	r := &fakeRunner{}
	return &Manager{UnitDir: t.TempDir(), Runner: r, Out: io.Discard, Observe: func(context.Context, time.Duration) error { return nil }}, r
}

func TestInstallLifecycle(t *testing.T) {
	m, r := testManager(t)
	c := testConfig(t)
	if err := m.Install(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.UnitDir, "admin.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), Marker) {
		t.Fatal("missing marker")
	}
	calls := strings.Join(r.calls, "\n")
	for _, want := range []string{"systemd-analyze verify", "systemctl daemon-reload", "systemctl enable --now admin.service", "systemctl show --property=ActiveState"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %s: %s", want, calls)
		}
	}
	if err := m.Install(context.Background(), c); err == nil {
		t.Fatal("overwrote existing unit")
	}
	got, _ := os.ReadFile(filepath.Join(m.UnitDir, "admin.service"))
	if string(got) != string(data) {
		t.Fatal("changed existing unit")
	}
}

func TestInstallRefusesVendorUnit(t *testing.T) {
	m, r := testManager(t)
	r.existing = true
	if m.Install(context.Background(), testConfig(t)) == nil {
		t.Fatal("accepted vendor unit")
	}
	if len(r.calls) != 1 {
		t.Fatal(r.calls)
	}
}

func TestInstallFailures(t *testing.T) {
	for _, fail := range []string{"--property=LoadState", "systemd-analyze verify", "daemon-reload", "enable --now"} {
		t.Run(fail, func(t *testing.T) {
			m, r := testManager(t)
			r.fail = fail
			if err := m.Install(context.Background(), testConfig(t)); err == nil {
				t.Fatal("expected error")
			}
			if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); !os.IsNotExist(err) {
				t.Fatalf("unit left: %v", err)
			}
		})
	}
}

func TestFailedProcessRollsBack(t *testing.T) {
	m, r := testManager(t)
	r.inactive = true
	if m.Install(context.Background(), testConfig(t)) == nil {
		t.Fatal("reported failed service as success")
	}
	if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); !os.IsNotExist(err) {
		t.Fatal("unit left")
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "disable --now admin.service") {
		t.Fatal(r.calls)
	}
}

func TestManagementProtectsUnmanagedFiles(t *testing.T) {
	for _, kind := range []string{"unmarked", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			m, r := testManager(t)
			path := filepath.Join(m.UnitDir, "admin.service")
			if kind == "unmarked" {
				os.WriteFile(path, []byte("[Service]\n"), 0644)
			}
			if kind == "symlink" {
				os.Symlink("/dev/null", path)
			}
			for _, action := range []string{"start", "stop", "restart", "status", "uninstall", "logs"} {
				if m.Action(context.Background(), action, "admin", false) == nil {
					t.Errorf("accepted %s", action)
				}
			}
			if len(r.calls) > 0 {
				t.Fatal(r.calls)
			}
		})
	}
}

func TestManageAndUninstall(t *testing.T) {
	m, r := testManager(t)
	c := testConfig(t)
	if err := m.Install(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "stop", "restart", "status", "logs"} {
		if err := m.Action(context.Background(), action, "admin", action == "logs"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Action(context.Background(), "uninstall", "admin", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Executable); err != nil {
		t.Fatal("deleted application", err)
	}
	if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); !os.IsNotExist(err) {
		t.Fatal("unit remains")
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "--follow") {
		t.Fatal("logs do not follow")
	}
}

func TestRollbackFailurePreservesUnit(t *testing.T) {
	m, r := testManager(t)
	r.inactive = true
	r.fail = "disable --now"
	err := m.Install(context.Background(), testConfig(t))
	if err == nil || !strings.Contains(err.Error(), "保留") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); err != nil {
		t.Fatal("lost recovery unit", err)
	}
}

func TestActionFailures(t *testing.T) {
	for _, action := range []string{"start", "restart", "stop", "status", "logs", "uninstall", "unknown"} {
		t.Run(action, func(t *testing.T) {
			m, r := testManager(t)
			os.WriteFile(filepath.Join(m.UnitDir, "admin.service"), []byte(Marker), 0644)
			r.fail = "systemctl"
			if action == "logs" {
				r.fail = "journalctl"
			}
			if m.Action(context.Background(), action, "admin", false) == nil {
				t.Fatal("lost error")
			}
			if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); err != nil {
				t.Fatal("removed unit on command failure")
			}
		})
	}
}

func TestObservationFailureRollsBack(t *testing.T) {
	m, _ := testManager(t)
	m.Observe = func(context.Context, time.Duration) error { return context.Canceled }
	if m.Install(context.Background(), testConfig(t)) == nil {
		t.Fatal("lost cancellation")
	}
	if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); !os.IsNotExist(err) {
		t.Fatal("unit left")
	}
}

func TestInstallRejectsSymlinkAndInvalidConfig(t *testing.T) {
	m, r := testManager(t)
	c := testConfig(t)
	c.Name = "../bad"
	if m.Install(context.Background(), c) == nil {
		t.Fatal("accepted name")
	}
	c.Name = "admin"
	os.Symlink("/dev/null", filepath.Join(m.UnitDir, "admin.service"))
	if m.Install(context.Background(), c) == nil {
		t.Fatal("overwrote mask")
	}
	if len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
	if m.Action(context.Background(), "stop", "../bad", false) == nil {
		t.Fatal("accepted name")
	}
}
