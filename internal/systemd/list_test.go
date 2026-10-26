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
		"z-active.service":  "UnitFileState=enabled\nActiveState=active\n",
		"a-stopped.service": "ActiveState=inactive\nUnitFileState=disabled\n",
		"m-failed.service":  "ActiveState=failed\nUnitFileState=enabled\n",
	}
	if err := m.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(out.String())
	want := "NAME STATE AUTOSTART a-stopped inactive disabled m-failed failed enabled z-active active enabled"
	if strings.Join(got, " ") != want {
		t.Fatalf("unexpected list:\n%s", out.String())
	}
	if len(r.calls) != 3 {
		t.Fatalf("queried unmanaged services: %v", r.calls)
	}
	for _, call := range r.calls {
		if !strings.HasPrefix(call, "systemctl show ") {
			t.Fatalf("list performed a write operation: %s", call)
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
