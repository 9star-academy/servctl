package systemd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	unitID           string
	states           map[string]string
	unitFiles        string
}

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	s := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, s)
	if r.fail != "" && strings.Contains(s, r.fail) {
		return []byte("simulated failure"), errors.New("exit 1")
	}
	if strings.Contains(s, "list-unit-files") {
		return []byte(r.unitFiles), nil
	}
	if strings.Contains(s, "--property=UnitFileState") {
		units := args
		if idx := slices.Index(args, "--"); idx >= 0 {
			units = args[idx+1:]
		} else {
			units = args[len(args)-1:]
		}
		var blocks []string
		for _, unit := range units {
			blocks = append(blocks, strings.TrimRight(r.states[unit], "\n"))
		}
		return []byte(strings.Join(blocks, "\n\n") + "\n"), nil
	}
	if strings.Contains(s, "--property=LoadState") {
		if r.existing {
			return []byte("loaded\n"), nil
		}
		return []byte("not-found\n"), nil
	}
	if strings.Contains(s, "--property=Id") && r.unitID != "" {
		return []byte("Id=" + r.unitID + "\n"), nil
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
	if err := m.Action(context.Background(), "start", "admin", ReadyCheck{}); err != nil {
		t.Fatal(err)
	}
}
func (r *fakeRunner) Stream(ctx context.Context, stdout, _ io.Writer, name string, args ...string) error {
	out, err := r.Output(ctx, name, args...)
	_, _ = stdout.Write(out)
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
	var observed []time.Duration
	m.Observe = func(_ context.Context, d time.Duration) error {
		observed = append(observed, d)
		return nil
	}
	if m.Install(context.Background(), testConfig(t)) == nil {
		t.Fatal("reported failed service as success")
	}
	if len(observed) != 1 || observed[0] >= time.Second {
		t.Fatalf("expected fast-fail observation, got %v", observed)
	}
	if _, err := os.Stat(filepath.Join(m.UnitDir, "admin.service")); !os.IsNotExist(err) {
		t.Fatal("unit left")
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "disable --now admin.service") {
		t.Fatal(r.calls)
	}
}

func TestUninstallProtectsUnmanagedFiles(t *testing.T) {
	for _, kind := range []string{"unmarked", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			m, r := testManager(t)
			path := filepath.Join(m.UnitDir, "admin.service")
			if kind == "unmarked" {
				if err := os.WriteFile(path, []byte("[Service]\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Symlink("/dev/null", path); err != nil {
					t.Fatal(err)
				}
			}
			if m.Action(context.Background(), "uninstall", "admin", ReadyCheck{}) == nil {
				t.Fatal("uninstalled unmanaged service")
			}
			if len(r.calls) > 0 {
				t.Fatal(r.calls)
			}
		})
	}
}

func TestActionsForExistingSystemServices(t *testing.T) {
	m, r := testManager(t)
	// Only the alias is local. Systemd can resolve the actual unit elsewhere.
	if err := os.Symlink("/usr/lib/systemd/system/worker.service", filepath.Join(m.UnitDir, "worker-alias.service")); err != nil {
		t.Fatal(err)
	}
	r.unitID = "worker.service"
	m.Observe = func(context.Context, time.Duration) error {
		return errors.New("unexpected active-state check for external unit")
	}
	for _, tt := range []struct {
		action string
		name   string
		follow bool
		want   []string
	}{
		{"status", "worker-alias.service", false, []string{"systemctl status --no-pager --full -- worker-alias.service"}},
		{"show", "worker-alias.service", false, []string{"systemctl cat --no-pager -- worker-alias.service"}},
		{"enable", "worker-alias", false, []string{"systemctl enable -- worker-alias.service"}},
		{"disable", "worker-alias.service", false, []string{"systemctl disable -- worker-alias.service"}},
		{"logs", "worker-alias", true, []string{
			"systemctl show --property=Id -- worker-alias.service",
			"journalctl --unit=worker.service --lines 50 --no-pager --follow",
		}},
		{"start", "worker@backup.service", false, []string{"systemctl start -- worker@backup.service"}},
		{"restart", "worker@backup", false, []string{"systemctl restart -- worker@backup.service"}},
		{"stop", "-worker", false, []string{"systemctl stop -- -worker.service"}},
	} {
		t.Run(tt.action, func(t *testing.T) {
			r.calls = nil
			var err error
			if tt.action == "logs" {
				err = m.Logs(context.Background(), tt.name, LogOptions{Follow: tt.follow})
			} else {
				err = m.Action(context.Background(), tt.action, tt.name, ReadyCheck{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.calls, tt.want) {
				t.Fatalf("calls = %q, want %q", r.calls, tt.want)
			}
		})
	}
}

func TestLogsUseRequestedUnitWhenIDLookupFails(t *testing.T) {
	m, r := testManager(t)
	r.fail = "--property=Id"
	if err := m.Logs(context.Background(), "worker.service", LogOptions{Lines: 100, Since: "1 hour ago", Level: "err", Follow: true}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl show --property=Id -- worker.service",
		"journalctl --unit=worker.service --lines 100 --no-pager --since=1 hour ago --priority=err --follow",
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	if m.Logs(context.Background(), "worker.service", LogOptions{Level: "invalid"}) == nil {
		t.Fatal("accepted invalid log level")
	}
}

func TestStartStillChecksManagedServiceState(t *testing.T) {
	m, r := testManager(t)
	if err := os.WriteFile(filepath.Join(m.UnitDir, "admin.service"), []byte(Marker), 0644); err != nil {
		t.Fatal(err)
	}
	r.inactive = true
	if err := m.Action(context.Background(), "start", "admin.service", ReadyCheck{}); err == nil {
		t.Fatal("reported failed managed service as started")
	}
}

func TestManageAndUninstall(t *testing.T) {
	m, r := testManager(t)
	c := testConfig(t)
	if err := m.Install(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "stop", "restart", "status", "show", "enable", "disable"} {
		if err := m.Action(context.Background(), action, "admin", ReadyCheck{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Logs(context.Background(), "admin", LogOptions{Follow: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Action(context.Background(), "uninstall", "admin", ReadyCheck{}); err != nil {
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
			var err error
			if action == "logs" {
				err = m.Logs(context.Background(), "admin", LogOptions{})
			} else {
				err = m.Action(context.Background(), action, "admin", ReadyCheck{})
			}
			if err == nil {
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
	if m.Action(context.Background(), "stop", "../bad", ReadyCheck{}) == nil {
		t.Fatal("accepted name")
	}
}

func TestInstallReplaceAndRollback(t *testing.T) {
	m, r := testManager(t)
	c := testConfig(t)
	c.Description = "v1"
	if err := m.Install(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(filepath.Join(m.UnitDir, "admin.service"))
	if err != nil {
		t.Fatal(err)
	}
	// Replace succeeds and restarts the unit.
	r.calls = nil
	c.Replace = true
	c.Description = "v2"
	if err := m.Install(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(filepath.Join(m.UnitDir, "admin.service"))
	if err != nil || !strings.Contains(string(updated), "Description=v2") {
		t.Fatalf("replace did not update unit: %v\n%s", err, updated)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "systemctl restart admin.service") {
		t.Fatalf("replace did not restart unit: %v", r.calls)
	}
	// Failed replace restores previous unit content.
	r.calls = nil
	r.inactive = true
	c.Description = "v3-bad"
	if err := m.Install(context.Background(), c); err == nil {
		t.Fatal("expected failed replace to return error")
	}
	restored, err := os.ReadFile(filepath.Join(m.UnitDir, "admin.service"))
	if err != nil || string(restored) != string(updated) {
		t.Fatalf("failed replace did not restore previous unit:\ngot:\n%s\nwant:\n%s", restored, updated)
	}
	// Replace refuses unmanaged foreign units.
	r.inactive = false
	foreignPath := filepath.Join(m.UnitDir, "foreign.service")
	if err := os.WriteFile(foreignPath, []byte("[Service]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c.Name = "foreign"
	if err := m.Install(context.Background(), c); err == nil {
		t.Fatal("replace overwrote unmanaged unit")
	}
	_ = orig
}

func TestInstallNoEnableAndNoStart(t *testing.T) {
	for _, tt := range []struct {
		name              string
		noEnable, noStart bool
		wantCall          string
		forbidCall        string
	}{
		{"no-enable only", true, false, "systemctl start admin.service", "systemctl enable"},
		{"no-start only", false, true, "systemctl enable admin.service", "ActiveState"},
		{"no-enable and no-start", true, true, "systemctl daemon-reload", "systemctl enable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, r := testManager(t)
			c := testConfig(t)
			c.NoEnable = tt.noEnable
			c.NoStart = tt.noStart
			if err := m.Install(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(r.calls, "\n")
			if !strings.Contains(joined, tt.wantCall) {
				t.Fatalf("missing %q in:\n%s", tt.wantCall, joined)
			}
			if strings.Contains(joined, tt.forbidCall) {
				t.Fatalf("unexpected %q in:\n%s", tt.forbidCall, joined)
			}
		})
	}
}
