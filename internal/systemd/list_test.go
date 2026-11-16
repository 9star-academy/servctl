package systemd

import (
	"bytes"
	"context"
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
	if err := m.List(context.Background()); err != nil {
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
	if len(r.calls) != 3 {
		t.Fatalf("queried unmanaged services: %v", r.calls)
	}
	const query = "systemctl show --property=ActiveState --property=UnitFileState --property=MainPID --property=ActiveEnterTimestampMonotonic --property=NRestarts --property=User --property=MemoryCurrent "
	for i, unit := range []string{"a-stopped.service", "m-failed.service", "z-active.service"} {
		if r.calls[i] != query+unit {
			t.Fatalf("unexpected list query: %s", r.calls[i])
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
	if err := m.List(context.Background()); err != nil {
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
			if err := m.List(context.Background()); err == nil {
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
