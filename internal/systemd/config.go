package systemd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Marker identifies files created by this tool, not arbitrary system services.
const Marker = "# Managed by servctl v1\n"

type Config struct {
	Name        string
	Executable  string
	WorkDir     string
	User        string
	Args        []string
	Env         []string
	EnvFiles    []string
	Description string
	Restart     string
	RestartSec  string
	MemoryMax   string
	CPUQuota    string
	After       []string
	NoEnable    bool
	NoStart     bool
	Replace     bool
	Ready       ReadyCheck
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
var unitName = regexp.MustCompile(`^[A-Za-z0-9:_.\\-]+(@[A-Za-z0-9:_.@\\-]+)?$`)
var userName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*\$?$`)
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var restartSecPattern = regexp.MustCompile(`^[0-9]+(ms|s|m|h)?$`)
var memoryMaxPattern = regexp.MustCompile(`^[1-9][0-9]*([KMGTPE]|%)?$`)
var cpuQuotaPattern = regexp.MustCompile(`^[1-9][0-9]*(\.[0-9]+)?%$`)
var afterUnitPattern = regexp.MustCompile(`^[A-Za-z0-9:_.\\-]+(@[A-Za-z0-9:_.@\\-]+)?\.(service|target|socket|mount|timer|path|slice|scope)$`)

var validRestartPolicies = map[string]bool{
	"no":          true,
	"on-success":  true,
	"on-failure":  true,
	"on-abnormal": true,
	"on-watchdog": true,
	"on-abort":    true,
	"always":      true,
}

func normalizeAfterUnit(unit string) (string, error) {
	if len(unit) > 255 {
		return "", fmt.Errorf("无效 --after 依赖单元 %q", unit)
	}
	if afterUnitPattern.MatchString(unit) {
		return unit, nil
	}
	u, err := ServiceUnitName(unit)
	if err != nil {
		return "", fmt.Errorf("无效 --after 依赖单元 %q：请指定有效的 systemd 单元名（如 postgresql.service 或 network-online.target）", unit)
	}
	return u, nil
}

func ValidateName(name string) error {
	if !serviceName.MatchString(name) || strings.HasSuffix(name, ".service") {
		return fmt.Errorf("无效服务名 %q：使用字母、数字、点、下划线或横线，以字母或数字开头，最多 200 字符，不带 .service", name)
	}
	return nil
}

// ServiceUnitName accepts a literal system service name, with or without its suffix.
func ServiceUnitName(name string) (string, error) {
	const suffix = ".service"
	base := strings.TrimSuffix(name, suffix)
	if len(base)+len(suffix) > 255 || !unitName.MatchString(base) {
		return "", fmt.Errorf("无效 systemd 服务名 %q：请指定一个服务名，模板服务需指定实例", name)
	}
	return base + suffix, nil
}

func (c Config) Validate() error {
	if err := ValidateName(c.Name); err != nil {
		return err
	}
	for label, path := range map[string]string{"程序": c.Executable, "工作目录": c.WorkDir} {
		if !filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
			return fmt.Errorf("%s必须是无控制字符的绝对路径", label)
		}
	}
	// systemd 255 strips trailing whitespace when passing WorkingDirectory to
	// its executor. A '/.' suffix is normalized away before that handoff.
	if strings.HasSuffix(filepath.Clean(c.WorkDir), " ") {
		return fmt.Errorf("工作目录末尾不能是空格：systemd 可能丢失末尾空格，导致服务启动失败")
	}
	execInfo, err := os.Stat(c.Executable)
	if err != nil {
		return fmt.Errorf("读取程序: %w", err)
	}
	if !execInfo.Mode().IsRegular() || execInfo.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("程序不是可执行普通文件: %s（请检查 chmod +x）", c.Executable)
	}
	workInfo, err := os.Stat(c.WorkDir)
	if err != nil {
		return fmt.Errorf("读取工作目录: %w", err)
	}
	if !workInfo.IsDir() {
		return fmt.Errorf("工作目录不是目录: %s", c.WorkDir)
	}
	if !userName.MatchString(c.User) {
		return fmt.Errorf("请通过 --user 指定有效的现有运行用户")
	}
	u, err := user.Lookup(c.User)
	if err != nil {
		return fmt.Errorf("查询运行用户 %s: %w", c.User, err)
	}
	if err := checkUserAccess(u, c.Executable, execInfo, c.WorkDir, workInfo); err != nil {
		return err
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) || !utf8.ValidString(arg) {
			return fmt.Errorf("程序参数必须是无 NUL 的 UTF-8 文本")
		}
	}
	for _, entry := range c.Env {
		key, val, ok := strings.Cut(entry, "=")
		if !ok || !envKey.MatchString(key) {
			return fmt.Errorf("无效环境变量 %q：必须为 KEY=VALUE 格式，且变量名仅含字母、数字或下划线", entry)
		}
		if strings.ContainsRune(val, 0) || !utf8.ValidString(val) {
			return fmt.Errorf("环境变量 %s 的值必须是无 NUL 的 UTF-8 文本", key)
		}
	}
	for _, envFile := range c.EnvFiles {
		if !filepath.IsAbs(envFile) || !utf8.ValidString(envFile) || strings.ContainsFunc(envFile, unicode.IsControl) {
			return fmt.Errorf("环境变量文件必须是无控制字符的绝对路径: %q", envFile)
		}
		if strings.HasSuffix(filepath.Clean(envFile), " ") {
			return fmt.Errorf("环境变量文件路径末尾不能是空格: %q", envFile)
		}
		fi, err := os.Stat(envFile)
		if err != nil {
			return fmt.Errorf("读取环境变量文件: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("环境变量文件不是普通文件: %s", envFile)
		}
	}
	if c.Description != "" && (!utf8.ValidString(c.Description) || strings.ContainsFunc(c.Description, unicode.IsControl)) {
		return fmt.Errorf("--desc 不能包含控制字符或无效 UTF-8")
	}
	if c.Restart != "" && !validRestartPolicies[c.Restart] {
		return fmt.Errorf("无效 --restart 策略 %q：可选 no、on-success、on-failure、on-abnormal、on-watchdog、on-abort、always", c.Restart)
	}
	if c.RestartSec != "" && !restartSecPattern.MatchString(c.RestartSec) {
		return fmt.Errorf("无效 --restart-sec %q：示例 3、5s、500ms", c.RestartSec)
	}
	if c.MemoryMax != "" && !memoryMaxPattern.MatchString(c.MemoryMax) {
		return fmt.Errorf("无效 --memory-max %q：示例 512M、1G、50%%", c.MemoryMax)
	}
	if c.CPUQuota != "" && !cpuQuotaPattern.MatchString(c.CPUQuota) {
		return fmt.Errorf("无效 --cpu-quota %q：示例 50%%、200%%", c.CPUQuota)
	}
	for _, dep := range c.After {
		if _, err := normalizeAfterUnit(dep); err != nil {
			return err
		}
	}
	if err := ValidateReadyCheck(c.Ready); err != nil {
		return err
	}
	return nil
}

// quoteWord follows systemd.syntax C-style escaping, not shell quoting.
func quoteWord(value string) string {
	return strings.ReplaceAll(strconv.Quote(strings.ReplaceAll(value, "%", "%%")), ";", `\x3b`)
}

func (c Config) Render() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	words := []string{quoteWord(c.Executable)}
	for _, arg := range c.Args {
		words = append(words, quoteWord(arg))
	}
	desc := c.Description
	if desc == "" {
		desc = "servctl service " + c.Name
	}
	restart := c.Restart
	if restart == "" {
		restart = "on-failure"
	}
	restartSec := c.RestartSec
	if restartSec == "" {
		restartSec = "3"
	}
	after := []string{"network.target"}
	seenAfter := map[string]bool{"network.target": true}
	for _, dep := range c.After {
		u, _ := normalizeAfterUnit(dep)
		if !seenAfter[u] {
			seenAfter[u] = true
			after = append(after, u)
		}
	}

	var b strings.Builder
	// ':' disables systemd environment substitution. Percent specifiers are
	// escaped separately. WorkingDirectory is a scalar path, not a quoted word;
	// '/.' keeps a final backslash from becoming a unit line continuation.
	fmt.Fprintf(&b, "%s[Unit]\n", Marker)
	fmt.Fprintf(&b, "Description=%s\n", strings.ReplaceAll(desc, "%", "%%"))
	fmt.Fprintf(&b, "After=%s\n", strings.Join(after, " "))
	b.WriteString("StartLimitIntervalSec=60\n")
	b.WriteString("StartLimitBurst=5\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=exec\n")
	fmt.Fprintf(&b, "User=%s\n", c.User)
	fmt.Fprintf(&b, "WorkingDirectory=%s/.\n", strings.ReplaceAll(c.WorkDir, "%", "%%"))
	for _, envFile := range c.EnvFiles {
		fmt.Fprintf(&b, "EnvironmentFile=%s\n", strings.ReplaceAll(envFile, "%", "%%"))
	}
	for _, entry := range c.Env {
		fmt.Fprintf(&b, "Environment=%s\n", quoteWord(entry))
	}
	fmt.Fprintf(&b, "ExecStart=:%s\n", strings.Join(words, " "))
	fmt.Fprintf(&b, "Restart=%s\n", restart)
	fmt.Fprintf(&b, "RestartSec=%s\n", restartSec)
	if c.MemoryMax != "" {
		fmt.Fprintf(&b, "MemoryMax=%s\n", c.MemoryMax)
	}
	if c.CPUQuota != "" {
		fmt.Fprintf(&b, "CPUQuota=%s\n", c.CPUQuota)
	}
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return []byte(b.String()), nil
}
