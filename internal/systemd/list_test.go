package systemd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListFiltersUnmanagedAndIncludesStoppedAndFailed(t *testing.T) {
	m, r := testManager(t)
	var out bytes.Buffer
	m.Out = &out
	for _, name := range []string{"z-active.service", "a-stopped.service", "m-failed.service", "notes.txt", "invalid@name.service"} {
		if err := os.WriteFile(filepath.Join(m.UnitDir, name), []byte(Marker+"[Service]\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"nginx.service", "ssh.service"} {
		if err := os.WriteFile(filepath.Join(m.UnitDir, name), []byte("[Service]\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(m.UnitDir, "z-active.service"), filepath.Join(m.UnitDir, "linked.service")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(m.UnitDir, "directory.service"), 0755); err != nil {
		t.Fatal(err)
	}
	r.states = map[string]string{
		"z-active.service":  "UnitFileState=enabled\nActiveState=active\nMainPID=24817\nNRestarts=0\nUser=ubuntu\nMemoryCurrent=88080384\n",
		"a-stopped.service": "ActiveState=inactive\nUnitFileState=disabled\nMainPID=0\nActiveEnterTimestampMonotonic=1\nNRestarts=0\nUser=ubuntu\nMemoryCurrent=[not set]\n",
		"m-failed.service":  "ActiveState=failed\nUnitFileState=enabled\nMainPID=0\nActiveEnterTimestampMonotonic=1\nNRestarts=5\nUser=deploy\n",
	}
	if err := m.List(context.Background(), ListOptions{}); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")
	for i, row := range rows {
		rows[i] = strings.Join(strings.Fields(row), " ")
	}
	want := "NAME STATE AUTOSTART PID UPTIME RESTARTS USER MEM\n" +
		"a-stopped inactive disabled - - 0 ubuntu -\n" +
		"m-failed failed enabled - - 5 deploy -\n" +
		"z-active active enabled 24817 - 0 ubuntu 84.0MiB"
	if strings.Join(rows, "\n") != want {
		t.Fatalf("unexpected list:\n%s", out.String())
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected single batched query, got %v", r.calls)
	}
	const wantQuery = "systemctl show --property=ActiveState --property=UnitFileState --property=MainPID --property=ActiveEnterTimestampMonotonic --property=NRestarts --property=User --property=MemoryCurrent -- a-stopped.service m-failed.service z-active.service"
	if r.calls[0] != wantQuery {
		t.Fatalf("unexpected list query: %s", r.calls[0])
	}
}

func TestListSkipsUnreadableForeignUnit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files")
	}
	m, r := testManager(t)
	var out bytes.Buffer
	m.Out = &out
	unreadable := filepath.Join(m.UnitDir, "secret.service")
	if err := os.WriteFile(unreadable, []byte("[Service]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0600) })
	if err := os.WriteFile(filepath.Join(m.UnitDir, "myapp.service"), []byte(Marker+"[Service]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r.states = map[string]string{
		"myapp.service": "ActiveState=active\nUnitFileState=enabled\nMainPID=123\nNRestarts=0\nUser=ubuntu\n",
	}
	if err := m.List(context.Background(), ListOptions{}); err != nil {
		t.Fatalf("unexpected error with unreadable foreign unit: %v", err)
	}
	if !strings.Contains(out.String(), "myapp") {
		t.Fatalf("missing managed service in output:\n%s", out.String())
	}
}

func TestListJSONFailedAndAll(t *testing.T) {
	m, r := testManager(t)
	var out bytes.Buffer
	m.Out = &out
	if err := os.WriteFile(filepath.Join(m.UnitDir, "app.service"), []byte(Marker+"[Service]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r.unitFiles = "app.service enabled\nnginx.service enabled\ngetty@.service static\n"
	r.states = map[string]string{
		"app.service":   "ActiveState=failed\nUnitFileState=enabled\nMainPID=0\nNRestarts=3\nUser=ubuntu\n",
		"nginx.service": "ActiveState=active\nUnitFileState=enabled\nMainPID=42\nNRestarts=0\nUser=www-data\n",
	}

	// --all --json includes managed & unmanaged and skips templates.
	if err := m.List(context.Background(), ListOptions{All: true, JSON: true}); err != nil {
		t.Fatal(err)
	}
	var got []ServiceSummary
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(got) != 2 || got[0].Name != "app" || !got[0].Managed || got[1].Name != "nginx" || got[1].Managed {
		t.Fatalf("unexpected json list: %+v", got)
	}

	// --all --failed filters to failed only.
	out.Reset()
	if err := m.List(context.Background(), ListOptions{All: true, Failed: true, JSON: true}); err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "app" || got[0].State != "failed" {
		t.Fatalf("unexpected failed list: %+v", got)
	}
}

func TestColorizeListOutput(t *testing.T) {
	table := "NAME    STATE     AUTOSTART\n" +
		"app     active    enabled\n" +
		"broken  failed    enabled\n" +
		"idle    inactive  disabled\n"
	items := []ServiceSummary{
		{Name: "app", State: "active"},
		{Name: "broken", State: "failed"},
		{Name: "idle", State: "inactive"},
	}
	colored := colorizeListOutput(table, items)
	for _, want := range []string{"\x1b[32mactive\x1b[0m", "\x1b[31mfailed\x1b[0m", "\x1b[2minactive\x1b[0m"} {
		if !strings.Contains(colored, want) {
			t.Fatalf("missing %q in %q", want, colored)
		}
	}
}

func TestListEmptyDoesNotQuerySystemd(t *testing.T) {
	m, r := testManager(t)
	var out bytes.Buffer
	m.Out = &out
	if err := os.WriteFile(filepath.Join(m.UnitDir, "foreign.service"), []byte("[Service]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.List(context.Background(), ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "暂无 servctl 创建的服务") || len(r.calls) != 0 {
		t.Fatalf("output %q, calls %v", out.String(), r.calls)
	}
}

func TestListReportsDirectoryAndSystemdErrors(t *testing.T) {
	for _, kind := range []string{"directory", "systemctl", "incomplete-state"} {
		t.Run(kind, func(t *testing.T) {
			m, r := testManager(t)
			var out bytes.Buffer
			m.Out = &out
			if kind == "directory" {
				m.UnitDir = filepath.Join(m.UnitDir, "missing")
			} else {
				if err := os.WriteFile(filepath.Join(m.UnitDir, "admin.service"), []byte(Marker), 0644); err != nil {
					t.Fatal(err)
				}
				if kind == "systemctl" {
					r.fail = "systemctl show"
				} else {
					r.states = map[string]string{"admin.service": "ActiveState=active\n"}
				}
			}
			if err := m.List(context.Background(), ListOptions{}); err == nil {
				t.Fatal("lost listing error")
			}
			if out.Len() != 0 {
				t.Fatalf("printed a successful list after an error: %q", out.String())
			}
		})
	}
}

func TestFormatListUptime(t *testing.T) {
	for _, tt := range []struct {
		name  string
		since string
		now   uint64
		want  string
	}{
		{"unset", "0", 1_000_000, "-"},
		{"future", "1000001", 1_000_000, "-"},
		{"just started", "1000000", 1_500_000, "0s"},
		{"seconds", "1000000", 43_000_000, "42s"},
		{"minutes", "1000000", 485_000_000, "8m4s"},
		{"hours", "1000000", 8_101_000_000, "2h15m"},
		{"days", "1000000", 273_601_000_000, "3d4h"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatListUptime(tt.since, tt.now); got != tt.want {
				t.Fatalf("uptime = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatListMemory(t *testing.T) {
	for _, tt := range []struct{ value, want string }{
		{"[not set]", "-"},
		{"18446744073709551615", "-"},
		{"0", "0B"},
		{"1536", "1.5KiB"},
		{"1610612736", "1.5GiB"},
	} {
		if got := formatListMemory(tt.value); got != tt.want {
			t.Errorf("memory %q = %q, want %q", tt.value, got, tt.want)
		}
	}
}
