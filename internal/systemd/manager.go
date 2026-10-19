package systemd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Manager struct {
	UnitDir string
	Runner  Runner
	Out     io.Writer
	Observe func(context.Context, time.Duration) error
}

func NewManager(out io.Writer) *Manager {
	return &Manager{UnitDir: "/etc/systemd/system", Runner: ExecRunner{}, Out: out, Observe: Wait}
}

func (m *Manager) Install(ctx context.Context, c Config) error {
	data, err := c.Render()
	if err != nil {
		return err
	}
	unit := c.Name + ".service"
	path := filepath.Join(m.UnitDir, unit)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("服务文件已存在，拒绝覆盖: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	state, err := m.Runner.Output(ctx, "systemctl", "show", "--property=LoadState", "--value", unit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) != "not-found" {
		return fmt.Errorf("服务 %s 已被 systemd 使用，拒绝覆盖", unit)
	}
	stage, err := os.MkdirTemp(m.UnitDir, ".servctl-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stagedPath := filepath.Join(stage, unit)
	if err = os.WriteFile(stagedPath, data, 0644); err != nil {
		return err
	}
	if _, err = m.Runner.Output(ctx, "systemd-analyze", "verify", stagedPath); err != nil {
		return fmt.Errorf("服务配置校验失败: %w", err)
	}
	// Hard-link publication is atomic and fails if any destination already exists.
	if err = os.Link(stagedPath, path); err != nil {
		return fmt.Errorf("创建服务文件（不会覆盖）: %w", err)
	}
	rollback := func(cause error) error {
		// Cleanup must still run if Ctrl-C cancelled the installation context.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, e := m.Runner.Output(cleanCtx, "systemctl", "disable", "--now", unit); e != nil {
			return errors.Join(cause, fmt.Errorf("回滚未完成，保留 %s，请检查服务状态: %w", path, e))
		}
		if e := os.Remove(path); e != nil {
			return errors.Join(cause, fmt.Errorf("清理服务文件: %w", e))
		}
		_, e := m.Runner.Output(cleanCtx, "systemctl", "daemon-reload")
		return errors.Join(cause, e)
	}
	if _, err = m.Runner.Output(ctx, "systemctl", "daemon-reload"); err != nil {
		return rollback(err)
	}
	if _, err = m.Runner.Output(ctx, "systemctl", "enable", "--now", unit); err != nil {
		m.diagnostics(unit)
		return rollback(err)
	}
	if err = m.checkRunning(ctx, unit, true); err != nil {
		m.diagnostics(unit)
		return rollback(err)
	}
	fmt.Fprintf(m.Out, "已创建 %s\n运行用户: %s\n工作目录: %s\n状态: active，已设置开机自启\n提示: 仅检查进程状态，请继续验证应用端口或健康接口。\n", path, c.User, c.WorkDir)
	return nil
}

func (m *Manager) checkRunning(ctx context.Context, unit string, freshInstall bool) error {
	if err := m.Observe(ctx, 2*time.Second); err != nil {
		return err
	}
	out, err := m.Runner.Output(ctx, "systemctl", "show", "--property=ActiveState", "--property=NRestarts", unit)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	// Existing services may have historical automatic restarts. Only a new
	// installation must have zero restarts during its initial observation.
	if values["ActiveState"] != "active" || (freshInstall && values["NRestarts"] != "0") {
		return fmt.Errorf("服务 %s 启动检查未通过: %s", unit, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) diagnostics(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := m.Runner.Output(ctx, "journalctl", "--unit", unit, "--lines", "30", "--no-pager")
	if len(out) > 0 {
		fmt.Fprintln(m.Out, string(out))
	}
	if err != nil {
		fmt.Fprintf(m.Out, "读取日志失败: %v\n", err)
	}
}

func (m *Manager) managedPath(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	path := filepath.Join(m.UnitDir, name+".service")
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("拒绝操作非普通服务文件: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(data), Marker) {
		return "", fmt.Errorf("拒绝操作非 servctl 管理的服务: %s", path)
	}
	return path, nil
}

func (m *Manager) Action(ctx context.Context, action, name string, follow bool) error {
	path, err := m.managedPath(name)
	if err != nil {
		return err
	}
	unit := name + ".service"
	switch action {
	case "logs":
		args := []string{"--unit", unit, "--lines", "50", "--no-pager"}
		if follow {
			args = append(args, "--follow")
		}
		return m.Runner.Stream(ctx, m.Out, "journalctl", args...)
	case "status":
		return m.Runner.Stream(ctx, m.Out, "systemctl", "status", "--no-pager", "--full", unit)
	case "start", "restart", "stop":
		if _, err = m.Runner.Output(ctx, "systemctl", action, unit); err != nil {
			m.diagnostics(unit)
			return err
		}
		if action != "stop" {
			if err = m.checkRunning(ctx, unit, false); err != nil {
				m.diagnostics(unit)
				return err
			}
		}
		fmt.Fprintf(m.Out, "%s: %s 完成\n", name, action)
		return nil
	case "uninstall":
		if _, err = m.Runner.Output(ctx, "systemctl", "disable", "--now", unit); err != nil {
			return err
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		if _, err = m.Runner.Output(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "已卸载服务 %s，业务文件已保留。\n", name)
		return nil
	default:
		return fmt.Errorf("未知操作: %s", action)
	}
}
