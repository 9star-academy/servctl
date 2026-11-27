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
		{"env missing equals", func(c *Config) { c.Env = []string{"NOEQUALS"} }},
		{"env bad key", func(c *Config) { c.Env = []string{"1BAD=val"} }},
		{"env NUL value", func(c *Config) { c.Env = []string{"KEY=bad\x00val"} }},
		{"env-file missing", func(c *Config) { c.EnvFiles = []string{"/no/such/servctl.env"} }},
		{"env-file directory", func(c *Config) { c.EnvFiles = []string{c.WorkDir} }},
		{"desc newline", func(c *Config) { c.Description = "bad\nExecStart=/evil" }},
		{"invalid restart", func(c *Config) { c.Restart = "forever" }},
		{"invalid restart-sec", func(c *Config) { c.RestartSec = "3d" }},
		{"invalid memory-max", func(c *Config) { c.MemoryMax = "512MB" }},
		{"invalid cpu-quota", func(c *Config) { c.CPUQuota = "50" }},
		{"invalid after", func(c *Config) { c.After = []string{"bad unit"} }},
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

func TestConfigValidationRejectsInaccessibleUserPermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}
	c := testConfig(t)
	// Mode 0001 has an execute bit set, but the owner (current user) is denied execute.
	if err := os.Chmod(c.Executable, 0001); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(c.Executable, 0755) })
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "无权执行程序") {
		t.Fatalf("expected user execute permission error, got %v", err)
	}
}

func TestRenderOptionalDirectives(t *testing.T) {
	c := testConfig(t)
	envFile := filepath.Join(c.WorkDir, "app.env")
	if err := os.WriteFile(envFile, []byte("SECRET=123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Description = "My App 100%"
	c.Env = []string{"PORT=8200", "NOTE=a b;100%"}
	c.EnvFiles = []string{envFile}
	c.Restart = "always"
	c.RestartSec = "5s"
	c.MemoryMax = "512M"
	c.CPUQuota = "150%"
	c.After = []string{"network.target", "postgresql.service", "redis"}
	data, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"Description=My App 100%%",
		"After=network.target postgresql.service redis.service",
		"EnvironmentFile=" + envFile,
		`Environment="PORT=8200"`,
		`Environment="NOTE=a b\x3b100%%"`,
		"Restart=always",
		"RestartSec=5s",
		"MemoryMax=512M",
		"CPUQuota=150%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
