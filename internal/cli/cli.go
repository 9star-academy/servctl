package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/9star-academy/servctl/internal/selfupdate"
	"github.com/9star-academy/servctl/internal/systemd"
)

const help = `servctl — 一条命令注册 systemd 服务

用法:
  servctl install NAME --exec PATH --user USER [选项...] [-- ARG...]
  servctl start|restart UNIT [--health-url URL] [--wait-port PORT] [--ready-timeout DUR]
  servctl stop|status|show|enable|disable UNIT
  servctl logs UNIT [-f|--follow] [-n|--lines N] [--since TIME] [--level LEVEL]
  servctl uninstall NAME
  servctl list [--json] [--all] [--failed]
  servctl update
  servctl --version

install 常用选项:
  --exec PATH          可执行文件路径（必填）
  --user USER          现有运行用户（必填）
  --workdir DIR        工作目录（默认当前目录）
  --env KEY=VAL        环境变量（可重复指定）
  --env-file PATH      环境变量文件（可重复指定，由 systemd 以 EnvironmentFile 读取）
  --desc TEXT          服务描述（默认 servctl service NAME）
  --restart POLICY     重启策略：no/on-success/on-failure/always 等（默认 on-failure）
  --restart-sec DUR    重启等待时间，如 3、5s、500ms（默认 3）
  --memory-max LIMIT   内存上限，如 512M、1G、50%
  --cpu-quota PCT      CPU 配额，如 50%、200%
  --after UNIT         额外启动顺序依赖，如 postgresql.service（可重复指定）
  --no-enable          不设置开机自启
  --no-start           仅创建服务，不立即启动
  --replace            原地更新已有的 servctl 托管服务（校验后原子替换，失败自动回滚）
  --health-url URL     启动后轮询 HTTP(S) 健康接口直至返回 2xx
  --wait-port PORT     启动后等待本机 TCP 端口监听就绪
  --ready-timeout DUR  就绪检查超时时间（默认 15s）

示例:
  sudo servctl install myapp --exec /www/wwwroot/myapp/myapp --workdir /www/wwwroot/myapp --user ubuntu
  sudo servctl install myapp --exec ./myapp --user ubuntu --env PORT=8200 --env-file ./.env --memory-max 512M --wait-port 8200 -- --port 8200
  sudo servctl install myapp --replace --exec ./myapp --user ubuntu --health-url http://127.0.0.1:8200/health
  servctl show myapp
  servctl status myapp
  sudo servctl enable myapp
  servctl list --json
  sudo servctl update
  sudo servctl logs myapp -f -n 100 --since "1 hour ago" --level err

NAME 不带 .service；install 的 --user 必填，必须是现有用户。
UNIT 可以是已有的系统服务名，可带 .service 或实例名（如 getty@tty1.service）。
--exec、--workdir、--env-file 相对路径均以执行命令时的目录为准。
install 默认自动启动并设置开机自启；同名服务默认拒绝覆盖（可用 --replace 原地更新托管服务）。
程序需可执行且以前台方式运行；不会编译源码或配置端口。
uninstall 仅卸载 servctl 创建的服务，保留业务文件。
list 默认列出 servctl 创建的服务，可用 --all 查看系统全部服务、--failed 仅看失败服务、--json 输出 JSON。
update 检查最新正式版，发现新版本时校验并更新当前 servctl 程序。
修改系统服务需要 sudo；update 需要写入程序所在目录的权限。
日志权限不足时请使用 sudo。
`

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type Backend interface {
	Install(context.Context, systemd.Config) error
	Action(context.Context, string, string, systemd.ReadyCheck) error
	Logs(context.Context, string, systemd.LogOptions) error
	List(context.Context, systemd.ListOptions) error
}

type App struct {
	Backend            Backend
	Out, Err           io.Writer
	Version, GOOS, Cwd string
	EUID               int
	CheckSystem        func(string) error
	update             func(context.Context, string, io.Writer) error
}

type silentExitError struct {
	code int
}

func (e silentExitError) Error() string { return "" }

func New(version string, out, errOut io.Writer) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &App{
		Backend: systemd.NewManager(out, errOut), Out: out, Err: errOut,
		Version: version, GOOS: runtime.GOOS, EUID: os.Geteuid(), Cwd: cwd,
		CheckSystem: checkSystem, update: selfupdate.Run,
	}, nil
}

func checkSystem(action string) error {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return fmt.Errorf("系统未运行 systemd（容器需以 systemd 启动）: %w", err)
	}
	tools := []string{"systemctl"}
	switch action {
	case "install":
		tools = append(tools, "systemd-analyze")
	case "logs":
		tools = append(tools, "journalctl")
	}
	for _, name := range tools {
		if _, err := exec.LookPath(name); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) Run(ctx context.Context, args []string) int {
	err := a.run(ctx, args)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var silent silentExitError
	if errors.As(err, &silent) {
		return silent.code
	}
	fmt.Fprintf(a.Err, "错误: %v\n", err)
	if ctx.Err() != nil {
		return 130
	}
	return 1
}

func (a *App) run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(a.Out, help)
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(a.Out, "servctl %s\n", a.Version)
		return nil
	}
	action := args[0]
	switch action {
	case "install", "start", "stop", "restart", "status", "show", "enable", "disable", "logs", "uninstall", "list", "update":
	default:
		return fmt.Errorf("未知命令 %q，运行 servctl --help 查看用法", action)
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(a.Out, help)
		return nil
	}
	if action == "update" {
		if len(args) != 1 {
			return fmt.Errorf("update 不接受服务名或其他参数")
		}
		if a.GOOS != "linux" {
			return fmt.Errorf("自更新仅支持 Linux，当前系统为 %s", a.GOOS)
		}
		err := a.update(ctx, a.Version, a.Out)
		if a.EUID != 0 && errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("更新需要写入安装目录的权限，请使用 sudo servctl update: %w", err)
		}
		return err
	}
	name := ""
	rest := args[1:]
	if action != "list" {
		if len(args) < 2 {
			return fmt.Errorf("缺少服务名，运行 servctl --help 查看用法")
		}
		name = args[1]
		if action == "install" || action == "uninstall" {
			if err := systemd.ValidateName(name); err != nil {
				return err
			}
		} else if _, err := systemd.ServiceUnitName(name); err != nil {
			return err
		}
		rest = args[2:]
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprint(a.Out, help) }
	c := systemd.Config{Name: name}
	logOpts := systemd.LogOptions{Lines: 50}
	listOpts := systemd.ListOptions{}
	readyOpts := systemd.ReadyCheck{Timeout: 15 * time.Second}
	var envFlags, envFileFlags, afterFlags stringSliceFlag
	if action == "install" {
		fs.StringVar(&c.Executable, "exec", "", "可执行文件路径（必填）")
		fs.StringVar(&c.WorkDir, "workdir", a.Cwd, "工作目录")
		fs.StringVar(&c.User, "user", "", "现有运行用户（必填）")
		fs.Var(&envFlags, "env", "环境变量 KEY=VALUE（可重复指定）")
		fs.Var(&envFileFlags, "env-file", "环境变量文件路径（可重复指定）")
		fs.StringVar(&c.Description, "desc", "", "服务描述")
		fs.StringVar(&c.Restart, "restart", "", "重启策略")
		fs.StringVar(&c.RestartSec, "restart-sec", "", "重启等待时间")
		fs.StringVar(&c.MemoryMax, "memory-max", "", "内存上限")
		fs.StringVar(&c.CPUQuota, "cpu-quota", "", "CPU 配额")
		fs.Var(&afterFlags, "after", "启动顺序依赖单元（可重复指定）")
		fs.BoolVar(&c.NoEnable, "no-enable", false, "不设置开机自启")
		fs.BoolVar(&c.NoStart, "no-start", false, "仅创建服务，不立即启动")
		fs.BoolVar(&c.Replace, "replace", false, "原地替换已有的 servctl 托管服务")
		fs.StringVar(&c.Ready.HealthURL, "health-url", "", "健康检查 HTTP(S) 地址")
		fs.IntVar(&c.Ready.WaitPort, "wait-port", 0, "等待本机 TCP 端口就绪")
		fs.DurationVar(&c.Ready.Timeout, "ready-timeout", 15*time.Second, "就绪检查超时时间")
		for i, arg := range rest {
			if arg == "--" {
				c.Args = append([]string{}, rest[i+1:]...)
				rest = rest[:i]
				break
			}
		}
	} else if action == "logs" {
		fs.BoolVar(&logOpts.Follow, "follow", false, "持续查看日志，Ctrl-C 退出")
		fs.BoolVar(&logOpts.Follow, "f", false, "持续查看日志，Ctrl-C 退出")
		fs.IntVar(&logOpts.Lines, "lines", 50, "显示最近 N 行日志")
		fs.IntVar(&logOpts.Lines, "n", 50, "显示最近 N 行日志")
		fs.StringVar(&logOpts.Since, "since", "", "起始时间，如 \"1 hour ago\"、today")
		fs.StringVar(&logOpts.Level, "level", "", "日志级别，如 err、warning、info")
	} else if action == "list" {
		fs.BoolVar(&listOpts.JSON, "json", false, "以 JSON 格式输出")
		fs.BoolVar(&listOpts.All, "all", false, "列出全部 systemd 服务（含非 servctl 托管）")
		fs.BoolVar(&listOpts.Failed, "failed", false, "仅显示失败的服务")
	} else if action == "start" || action == "restart" {
		fs.StringVar(&readyOpts.HealthURL, "health-url", "", "健康检查 HTTP(S) 地址")
		fs.IntVar(&readyOpts.WaitPort, "wait-port", 0, "等待本机 TCP 端口就绪")
		fs.DurationVar(&readyOpts.Timeout, "ready-timeout", 15*time.Second, "就绪检查超时时间")
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		if action == "list" {
			return fmt.Errorf("list 不接受服务名或其他位置参数")
		}
		return fmt.Errorf("多余参数 %q；应用启动参数必须放在 -- 后", strings.Join(fs.Args(), " "))
	}
	if action == "install" {
		if c.Executable == "" || c.User == "" {
			return fmt.Errorf("install 必须提供 --exec 和 --user")
		}
		if c.WorkDir == "" {
			return fmt.Errorf("--workdir 不能为空")
		}
		if !filepath.IsAbs(c.Executable) {
			c.Executable = filepath.Join(a.Cwd, c.Executable)
		}
		if !filepath.IsAbs(c.WorkDir) {
			c.WorkDir = filepath.Join(a.Cwd, c.WorkDir)
		}
		c.Env = []string(envFlags)
		for _, ef := range envFileFlags {
			if ef == "" {
				return fmt.Errorf("--env-file 不能为空")
			}
			if !filepath.IsAbs(ef) {
				ef = filepath.Join(a.Cwd, ef)
			}
			c.EnvFiles = append(c.EnvFiles, ef)
		}
		c.After = []string(afterFlags)
		if err := systemd.ValidateReadyCheck(c.Ready); err != nil {
			return err
		}
	}
	if action == "start" || action == "restart" {
		if err := systemd.ValidateReadyCheck(readyOpts); err != nil {
			return err
		}
	}
	if action == "logs" {
		if logOpts.Lines <= 0 {
			return fmt.Errorf("--lines 必须大于 0")
		}
		if err := systemd.ValidateLogOptions(logOpts); err != nil {
			return err
		}
	}
	if a.GOOS != "linux" {
		return fmt.Errorf("服务管理仅支持 Linux + systemd，当前系统为 %s", a.GOOS)
	}
	if action != "status" && action != "show" && action != "logs" && action != "list" && a.EUID != 0 {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo servctl %s ...", action)
	}
	if err := a.CheckSystem(action); err != nil {
		return err
	}
	if action == "install" {
		return a.Backend.Install(ctx, c)
	}
	if action == "list" {
		return a.Backend.List(ctx, listOpts)
	}
	if action == "logs" {
		err := a.Backend.Logs(ctx, name, logOpts)
		if err != nil && logOpts.Follow && ctx.Err() != nil {
			return silentExitError{code: 130}
		}
		return err
	}
	err := a.Backend.Action(ctx, action, name, readyOpts)
	if err != nil && action == "status" {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return silentExitError{code: exitErr.ExitCode()}
		}
	}
	return err
}
