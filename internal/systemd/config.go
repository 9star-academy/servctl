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
	Name       string
	Executable string
	WorkDir    string
	User       string
	Args       []string
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
var userName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*\$?$`)

func ValidateName(name string) error {
	if !serviceName.MatchString(name) || strings.HasSuffix(name, ".service") {
		return fmt.Errorf("无效服务名 %q：使用字母、数字、点、下划线或横线，以字母或数字开头，最多 200 字符，不带 .service", name)
	}
	return nil
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
	info, err := os.Stat(c.Executable)
	if err != nil {
		return fmt.Errorf("读取程序: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("程序不是可执行普通文件: %s（请检查 chmod +x）", c.Executable)
	}
	info, err = os.Stat(c.WorkDir)
	if err != nil {
		return fmt.Errorf("读取工作目录: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("工作目录不是目录: %s", c.WorkDir)
	}
	if !userName.MatchString(c.User) {
		return fmt.Errorf("请通过 --user 指定有效的现有运行用户")
	}
	if _, err = user.Lookup(c.User); err != nil {
		return fmt.Errorf("查询运行用户 %s: %w", c.User, err)
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) || !utf8.ValidString(arg) {
			return fmt.Errorf("程序参数必须是无 NUL 的 UTF-8 文本")
		}
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
	// ':' disables systemd environment substitution. Percent specifiers are
	// escaped separately. WorkingDirectory is a scalar path, not a quoted word;
	// '/.' preserves a directory ending in whitespace or a backslash.
	return []byte(fmt.Sprintf(`%s[Unit]
Description=servctl service %s
After=network.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=exec
User=%s
WorkingDirectory=%s/.
ExecStart=:%s
Restart=on-failure
RestartSec=3
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, Marker, c.Name, c.User, strings.ReplaceAll(c.WorkDir, "%", "%%"), strings.Join(words, " "))), nil
}
