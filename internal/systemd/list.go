package systemd

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

// List reports all managed unit files, including inactive and failed services.
func (m *Manager) List(ctx context.Context) error {
	entries, err := os.ReadDir(m.UnitDir)
	if err != nil {
		return fmt.Errorf("读取服务目录: %w", err)
	}
	var rows []string
	// ReadDir sorts entries by filename, so the output order is stable.
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
			return fmt.Errorf("读取服务文件 %s: %w", path, err)
		}
		if !strings.HasPrefix(string(data), Marker) {
			continue
		}
		out, err := m.Runner.Output(ctx, "systemctl", "show",
			"--property=ActiveState", "--property=UnitFileState", "--property=MainPID",
			"--property=ActiveEnterTimestampMonotonic", "--property=NRestarts",
			"--property=User", "--property=MemoryCurrent", entry.Name())
		if err != nil {
			return fmt.Errorf("读取服务 %s 状态: %w", name, err)
		}
		values := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = value
			}
		}
		state, autostart := values["ActiveState"], values["UnitFileState"]
		if state == "" || autostart == "" {
			return fmt.Errorf("服务 %s 未返回完整的运行状态或开机自启状态", name)
		}
		pid := cmp.Or(values["MainPID"], "-")
		if pid == "0" {
			pid = "-"
		}
		uptime := "-"
		if state == "active" {
			uptime = formatListUptime(values["ActiveEnterTimestampMonotonic"], monotonicMicroseconds())
		}
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name, state, autostart, pid, uptime, cmp.Or(values["NRestarts"], "-"),
			cmp.Or(values["User"], "-"), formatListMemory(values["MemoryCurrent"])))
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(m.Out, "暂无 servctl 创建的服务。")
		return err
	}
	w := tabwriter.NewWriter(m.Out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSTATE\tAUTOSTART\tPID\tUPTIME\tRESTARTS\tUSER\tMEM"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprint(w, row); err != nil {
			return err
		}
	}
	return w.Flush()
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
