package systemd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

type ListOptions struct {
	JSON   bool
	All    bool
	Failed bool
}

type ServiceSummary struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Autostart string `json:"autostart"`
	PID       string `json:"pid"`
	Uptime    string `json:"uptime"`
	Restarts  string `json:"restarts"`
	User      string `json:"user"`
	Memory    string `json:"memory"`
	Managed   bool   `json:"managed"`
}

func (m *Manager) managedSet() (map[string]bool, error) {
	entries, err := os.ReadDir(m.UnitDir)
	if err != nil {
		return nil, fmt.Errorf("读取服务目录: %w", err)
	}
	managed := map[string]bool{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".service")
		if ValidateName(name) != nil {
			continue
		}
		path := filepath.Join(m.UnitDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				continue
			}
			return nil, fmt.Errorf("读取服务文件 %s: %w", path, err)
		}
		if strings.HasPrefix(string(data), Marker) {
			managed[entry.Name()] = true
		}
	}
	return managed, nil
}

// List reports managed (or all, when opts.All is set) unit files.
func (m *Manager) List(ctx context.Context, opts ListOptions) error {
	managed, err := m.managedSet()
	if err != nil {
		return err
	}
	var units []string
	if opts.All {
		out, err := m.Runner.Output(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
		if err != nil {
			return fmt.Errorf("列出系统服务: %w", err)
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			unit := fields[0]
			if !strings.HasSuffix(unit, ".service") || strings.HasSuffix(unit, "@.service") || seen[unit] {
				continue
			}
			seen[unit] = true
			units = append(units, unit)
		}
		slices.Sort(units)
	} else {
		for unit := range managed {
			units = append(units, unit)
		}
		slices.Sort(units)
	}
	items := make([]ServiceSummary, 0, len(units))
	if len(units) > 0 {
		args := append([]string{
			"show",
			"--property=ActiveState", "--property=UnitFileState", "--property=MainPID",
			"--property=ActiveEnterTimestampMonotonic", "--property=NRestarts",
			"--property=User", "--property=MemoryCurrent",
			"--",
		}, units...)
		out, err := m.Runner.Output(ctx, "systemctl", args...)
		if err != nil {
			return fmt.Errorf("读取服务状态: %w", err)
		}
		blocks := parseShowBlocks(string(out))
		if len(blocks) != len(units) {
			return fmt.Errorf("systemctl show 返回的服务数量不匹配: 实际 %d，预期 %d", len(blocks), len(units))
		}
		now := monotonicMicroseconds()
		for i, values := range blocks {
			unit := units[i]
			name := strings.TrimSuffix(unit, ".service")
			state, autostart := values["ActiveState"], values["UnitFileState"]
			if state == "" || autostart == "" {
				return fmt.Errorf("服务 %s 未返回完整的运行状态或开机自启状态", name)
			}
			if opts.Failed && state != "failed" {
				continue
			}
			pid := cmp.Or(values["MainPID"], "-")
			if pid == "0" {
				pid = "-"
			}
			uptime := "-"
			if state == "active" {
				uptime = formatListUptime(values["ActiveEnterTimestampMonotonic"], now)
			}
			items = append(items, ServiceSummary{
				Name:      name,
				State:     state,
				Autostart: autostart,
				PID:       pid,
				Uptime:    uptime,
				Restarts:  cmp.Or(values["NRestarts"], "-"),
				User:      cmp.Or(values["User"], "-"),
				Memory:    formatListMemory(values["MemoryCurrent"]),
				Managed:   managed[unit],
			})
		}
	}
	if opts.JSON {
		enc := json.NewEncoder(m.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}
	if len(items) == 0 {
		msg := "暂无 servctl 创建的服务。"
		if opts.Failed {
			msg = "暂无失败的服务。"
		} else if opts.All {
			msg = "暂无 systemd 服务。"
		}
		_, err := fmt.Fprintln(m.Out, msg)
		return err
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSTATE\tAUTOSTART\tPID\tUPTIME\tRESTARTS\tUSER\tMEM"); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			it.Name, it.State, it.Autostart, it.PID, it.Uptime, it.Restarts, it.User, it.Memory); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	rendered := buf.String()
	if shouldColorize(m.Out) {
		rendered = colorizeListOutput(rendered, items)
	}
	_, err = io.WriteString(m.Out, rendered)
	return err
}

func shouldColorize(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func colorizeState(state string) string {
	switch state {
	case "active":
		return "\x1b[32m" + state + "\x1b[0m"
	case "failed":
		return "\x1b[31m" + state + "\x1b[0m"
	case "inactive":
		return "\x1b[2m" + state + "\x1b[0m"
	default:
		return "\x1b[33m" + state + "\x1b[0m"
	}
}

func colorizeListOutput(table string, items []ServiceSummary) string {
	lines := strings.Split(table, "\n")
	for i, it := range items {
		rowIdx := i + 1
		if rowIdx >= len(lines) {
			break
		}
		line := lines[rowIdx]
		if len(line) <= len(it.Name) {
			continue
		}
		prefix := line[:len(it.Name)]
		rest := line[len(it.Name):]
		lines[rowIdx] = prefix + strings.Replace(rest, it.State, colorizeState(it.State), 1)
	}
	return strings.Join(lines, "\n")
}

func parseShowBlocks(out string) []map[string]string {
	var blocks []map[string]string
	var current map[string]string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			if current != nil {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		if current == nil {
			current = map[string]string{}
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			current[key] = value
		}
	}
	if current != nil {
		blocks = append(blocks, current)
	}
	return blocks
}

func formatListUptime(activeSince string, now uint64) string {
	since, err := strconv.ParseUint(activeSince, 10, 64)
	if err != nil || since == 0 || since > now {
		return "-"
	}
	seconds := (now - since) / 1_000_000
	const minute, hour, day = 60, 60 * 60, 24 * 60 * 60
	switch {
	case seconds >= day:
		return fmt.Sprintf("%dd%dh", seconds/day, seconds%day/hour)
	case seconds >= hour:
		return fmt.Sprintf("%dh%dm", seconds/hour, seconds%hour/minute)
	case seconds >= minute:
		return fmt.Sprintf("%dm%ds", seconds/minute, seconds%minute)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func formatListMemory(value string) string {
	bytes, err := strconv.ParseUint(value, 10, 64)
	if err != nil || bytes == math.MaxUint64 {
		return "-"
	}
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	size := float64(bytes) / 1024
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f%s", size, units[unit])
}
