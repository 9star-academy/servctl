package systemd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type LogOptions struct {
	Lines  int
	Follow bool
	Since  string
	Level  string
}

var validLogLevels = map[string]bool{
	"emerg": true, "alert": true, "crit": true, "err": true,
	"warning": true, "notice": true, "info": true, "debug": true,
	"0": true, "1": true, "2": true, "3": true,
	"4": true, "5": true, "6": true, "7": true,
}

func ValidateLogOptions(opts LogOptions) error {
	if opts.Lines < 0 {
		return fmt.Errorf("--lines 必须大于 0")
	}
	if opts.Since != "" && (!utf8.ValidString(opts.Since) || strings.ContainsFunc(opts.Since, unicode.IsControl)) {
		return fmt.Errorf("--since 不能包含控制字符或无效 UTF-8")
	}
	if opts.Level != "" && !validLogLevels[opts.Level] {
		return fmt.Errorf("无效 --level %q：可选 emerg、alert、crit、err、warning、notice、info、debug 或 0-7", opts.Level)
	}
	return nil
}

type Manager struct {
	UnitDir    string
	Runner     Runner
	Out        io.Writer
	Err        io.Writer
	Observe    func(context.Context, time.Duration) error
	Dial       func(context.Context, string, string) (net.Conn, error)
	HTTPClient *http.Client
}

func NewManager(out, errOut io.Writer) *Manager {
	return &Manager{UnitDir: "/etc/systemd/system", Runner: ExecRunner{}, Out: out, Err: errOut, Observe: Wait}
}

func (m *Manager) errWriter() io.Writer {
	if m.Err != nil {
		return m.Err
	}
	return m.Out
}

func (m *Manager) Install(ctx context.Context, c Config) error {
	data, err := c.Render()
	if err != nil {
		return err
	}
	unit := c.Name + ".service"
	path := filepath.Join(m.UnitDir, unit)
	replacing := false
	var oldData []byte
	if _, err := os.Lstat(path); err == nil {
		if !c.Replace {
			return fmt.Errorf("服务文件已存在，拒绝覆盖: %s（如需原地更新配置，请使用 --replace）", path)
		}
		if _, err := m.managedPath(c.Name); err != nil {
			return err
		}
		oldData, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取原有服务文件: %w", err)
		}
		replacing = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if !replacing {
		state, err := m.Runner.Output(ctx, "systemctl", "show", "--property=LoadState", "--value", unit)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(state)) != "not-found" {
			return fmt.Errorf("服务 %s 已被 systemd 使用，拒绝覆盖", unit)
		}
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
	if replacing {
		if err = os.Rename(stagedPath, path); err != nil {
			return fmt.Errorf("原子替换服务文件: %w", err)
		}
	} else {
		// Hard-link publication is atomic and fails if any destination already exists.
		if err = os.Link(stagedPath, path); err != nil {
			return fmt.Errorf("创建服务文件（不会覆盖）: %w", err)
		}
	}
	rollback := func(cause error) error {
		// Cleanup must still run if Ctrl-C cancelled the installation context.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if replacing {
			restoreStage, e := os.MkdirTemp(m.UnitDir, ".servctl-rollback-")
			if e != nil {
				return errors.Join(cause, fmt.Errorf("回滚创建临时目录失败: %w", e))
			}
			defer os.RemoveAll(restoreStage)
			restorePath := filepath.Join(restoreStage, unit)
			if e = os.WriteFile(restorePath, oldData, 0644); e != nil {
				return errors.Join(cause, fmt.Errorf("回滚写入原配置失败: %w", e))
			}
			if e = os.Rename(restorePath, path); e != nil {
				return errors.Join(cause, fmt.Errorf("回滚恢复 %s 失败: %w", path, e))
			}
			if _, e = m.Runner.Output(cleanCtx, "systemctl", "daemon-reload"); e != nil {
				return errors.Join(cause, fmt.Errorf("回滚重载 systemd 失败: %w", e))
			}
			if !c.NoStart {
				if _, e = m.Runner.Output(cleanCtx, "systemctl", "restart", unit); e != nil {
					return errors.Join(cause, fmt.Errorf("已恢复原配置，但重启原服务失败: %w", e))
				}
			}
			return cause
		}
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
	verb := "已创建"
	if replacing {
		verb = "已更新"
	}
	switch {
	case !c.NoEnable && !c.NoStart:
		if replacing {
			if _, err = m.Runner.Output(ctx, "systemctl", "enable", unit); err != nil {
				return rollback(err)
			}
			if _, err = m.Runner.Output(ctx, "systemctl", "restart", unit); err != nil {
				m.diagnostics(unit)
				return rollback(err)
			}
		} else {
			if _, err = m.Runner.Output(ctx, "systemctl", "enable", "--now", unit); err != nil {
				m.diagnostics(unit)
				return rollback(err)
			}
		}
		if err = m.checkRunning(ctx, unit, !replacing); err != nil {
			m.diagnostics(unit)
			return rollback(err)
		}
		if err = m.waitReady(ctx, c.Ready); err != nil {
			m.diagnostics(unit)
			return rollback(err)
		}
		if c.Ready.Enabled() {
			fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: active（就绪检查通过），已设置开机自启\n", verb, path, c.User, c.WorkDir)
		} else {
			fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: active，已设置开机自启\n提示: 仅检查进程状态，请继续验证应用端口或健康接口。\n", verb, path, c.User, c.WorkDir)
		}
	case c.NoEnable && !c.NoStart:
		op := "start"
		if replacing {
			op = "restart"
		}
		if _, err = m.Runner.Output(ctx, "systemctl", op, unit); err != nil {
			m.diagnostics(unit)
			return rollback(err)
		}
		if err = m.checkRunning(ctx, unit, !replacing); err != nil {
			m.diagnostics(unit)
			return rollback(err)
		}
		if err = m.waitReady(ctx, c.Ready); err != nil {
			m.diagnostics(unit)
			return rollback(err)
		}
		if c.Ready.Enabled() {
			fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: active（就绪检查通过），未设置开机自启\n", verb, path, c.User, c.WorkDir)
		} else {
			fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: active，未设置开机自启\n提示: 仅检查进程状态，请继续验证应用端口或健康接口。\n", verb, path, c.User, c.WorkDir)
		}
	case !c.NoEnable && c.NoStart:
		if _, err = m.Runner.Output(ctx, "systemctl", "enable", unit); err != nil {
			return rollback(err)
		}
		fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: 未启动，已设置开机自启\n", verb, path, c.User, c.WorkDir)
	default:
		fmt.Fprintf(m.Out, "%s %s\n运行用户: %s\n工作目录: %s\n状态: 未启动，未设置开机自启\n", verb, path, c.User, c.WorkDir)
	}
	return nil
}

func (m *Manager) checkRunning(ctx context.Context, unit string, freshInstall bool) error {
	const (
		observeTotal = 2 * time.Second
		observeStep  = 200 * time.Millisecond
	)
	var lastOut string
	var values map[string]string
	for elapsed := time.Duration(0); elapsed < observeTotal; elapsed += observeStep {
		if err := m.Observe(ctx, observeStep); err != nil {
			return err
		}
		out, err := m.Runner.Output(ctx, "systemctl", "show", "--property=ActiveState", "--property=NRestarts", unit)
		if err != nil {
			return err
		}
		lastOut = strings.TrimSpace(string(out))
		values = map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = value
			}
		}
		// Existing services may have historical automatic restarts. Only a new
		// installation must have zero restarts during its initial observation.
		if values["ActiveState"] == "failed" || values["ActiveState"] == "inactive" || (freshInstall && values["NRestarts"] != "0") {
			return fmt.Errorf("服务 %s 启动检查未通过: %s", unit, lastOut)
		}
	}
	if values["ActiveState"] != "active" {
		return fmt.Errorf("服务 %s 启动检查未通过: %s", unit, lastOut)
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

func (m *Manager) Logs(ctx context.Context, name string, opts LogOptions) error {
	if err := ValidateLogOptions(opts); err != nil {
		return err
	}
	unit, err := ServiceUnitName(name)
	if err != nil {
		return err
	}
	// Journald records the primary unit name, even when started through an alias.
	// The requested name can still find historical logs for a removed unit.
	if out, err := m.Runner.Output(ctx, "systemctl", "show", "--property=Id", "--", unit); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if id, ok := strings.CutPrefix(line, "Id="); ok {
				if canonical, err := ServiceUnitName(id); err == nil && canonical == id {
					unit = canonical
				}
				break
			}
		}
	}
	lines := opts.Lines
	if lines == 0 {
		lines = 50
	}
	args := []string{"--unit=" + unit, "--lines", strconv.Itoa(lines), "--no-pager"}
	if opts.Since != "" {
		args = append(args, "--since="+opts.Since)
	}
	if opts.Level != "" {
		args = append(args, "--priority="+opts.Level)
	}
	if opts.Follow {
		args = append(args, "--follow")
	}
	return m.Runner.Stream(ctx, m.Out, m.errWriter(), "journalctl", args...)
}

func (m *Manager) Action(ctx context.Context, action, name string, ready ReadyCheck) error {
	if action == "uninstall" {
		return m.uninstall(ctx, name)
	}
	unit, err := ServiceUnitName(name)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		return m.Runner.Stream(ctx, m.Out, m.errWriter(), "systemctl", "status", "--no-pager", "--full", "--", unit)
	case "show":
		return m.Runner.Stream(ctx, m.Out, m.errWriter(), "systemctl", "cat", "--no-pager", "--", unit)
	case "enable", "disable":
		if _, err := m.Runner.Output(ctx, "systemctl", action, "--", unit); err != nil {
			return err
		}
		base := strings.TrimSuffix(unit, ".service")
		fmt.Fprintf(m.Out, "%s: %s 完成\n", base, action)
		return nil
	case "start", "restart", "stop":
		if err := ValidateReadyCheck(ready); err != nil {
			return err
		}
		if _, err := m.Runner.Output(ctx, "systemctl", action, "--", unit); err != nil {
			m.diagnostics(unit)
			return err
		}
		base := strings.TrimSuffix(unit, ".service")
		if action == "start" || action == "restart" {
			// The active-state check applies to the long-running units servctl creates.
			// Other units can finish successfully without staying active (e.g. oneshot).
			if _, err := m.managedPath(base); err == nil {
				if err := m.checkRunning(ctx, unit, false); err != nil {
					m.diagnostics(unit)
					return err
				}
			}
			if err := m.waitReady(ctx, ready); err != nil {
				m.diagnostics(unit)
				return err
			}
		}
		fmt.Fprintf(m.Out, "%s: %s 完成\n", base, action)
		return nil
	default:
		return fmt.Errorf("未知操作: %s", action)
	}
}

func (m *Manager) uninstall(ctx context.Context, name string) error {
	path, err := m.managedPath(name)
	if err != nil {
		return err
	}
	unit := name + ".service"
	if _, err := m.Runner.Output(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if _, err := m.Runner.Output(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(m.Out, "已卸载服务 %s，业务文件已保留。\n", name)
	return nil
}
