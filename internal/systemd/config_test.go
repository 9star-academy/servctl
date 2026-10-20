package systemd

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "my app")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return Config{Name: "admin", Executable: path, WorkDir: dir, User: u.Username}
}

func TestNames(t *testing.T) {
	for _, name := range []string{"admin", "admin-api", "app_1", "app.v2"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "../ssh", "-bad", ".bad", "ssh.service", "x@y", "a/b", "a\nb", "hello world", strings.Repeat("a", 201)} {
		if ValidateName(name) == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing executable", func(c *Config) { c.Executable = "/no/such/servctl-app" }},
		{"directory executable", func(c *Config) { c.Executable = c.WorkDir }},
		{"not executable", func(c *Config) { os.Chmod(c.Executable, 0644) }},
		{"missing directory", func(c *Config) { c.WorkDir = "/no/such/servctl-dir" }},
		{"file directory", func(c *Config) { c.WorkDir = c.Executable }},
		{"missing user", func(c *Config) { c.User = "" }},
		{"unknown user", func(c *Config) { c.User = "servctl_no_such_user_12345" }},
		{"user injection", func(c *Config) { c.User = "root\nExecStart=/bad" }},
		{"argument NUL", func(c *Config) { c.Args = []string{"bad\x00arg"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConfig(t)
			tt.mutate(&c)
			if c.Validate() == nil {
				t.Fatal("expected error")
			}
		})
	}
	c := testConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRenderLiteralArguments(t *testing.T) {
	c := testConfig(t)
	c.Args = []string{"", "a b", `a"b`, `a\b`, "$HOME", "${HOME}", "%n", "line\nbreak", ";", "\\"}
	data, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{Marker, "Type=exec", "ExecStart=:", "Restart=on-failure", "WantedBy=multi-user.target", `"" "a b" "a\"b" "a\\b" "$HOME" "${HOME}" "%%n" "line\nbreak" "\x3b" "\\"`, "User=" + c.User} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestWorkingDirectoryTrailingSpace(t *testing.T) {
	c := testConfig(t)
	dir := filepath.Join(c.WorkDir, "app ")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/", "/."} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			c.WorkDir = dir + suffix
			_, err := c.Render()
			if err == nil || !strings.Contains(err.Error(), "末尾不能是空格") {
				t.Fatalf("expected trailing-space directory rejection, got %v", err)
			}
		})
	}

	// A space at the end of an intermediate component is preserved by systemd.
	c.WorkDir = filepath.Join(dir, "run")
	if err := os.Mkdir(c.WorkDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Render(); err != nil {
		t.Fatalf("rejected space in an intermediate component: %v", err)
	}
}
