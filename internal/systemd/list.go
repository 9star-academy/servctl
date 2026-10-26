package systemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
		out, err := m.Runner.Output(ctx, "systemctl", "show", "--property=ActiveState", "--property=UnitFileState", entry.Name())
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
		if values["ActiveState"] == "" || values["UnitFileState"] == "" {
			return fmt.Errorf("服务 %s 未返回完整的运行状态或开机自启状态", name)
		}
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\n", name, values["ActiveState"], values["UnitFileState"]))
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(m.Out, "暂无 servctl 创建的服务。")
		return err
	}
	w := tabwriter.NewWriter(m.Out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSTATE\tAUTOSTART"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprint(w, row); err != nil {
			return err
		}
	}
	return w.Flush()
}
