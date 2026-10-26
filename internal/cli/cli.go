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

	"github.com/9star-academy/servctl/internal/systemd"
)

const help = `servctl — 一条命令注册 systemd 服务

用法:
  servctl install NAME --exec PATH --user USER [--workdir DIR] [-- ARG...]
  servctl start|stop|restart|status|uninstall NAME
  servctl logs NAME [--follow]
  servctl list
  servctl --version

示例:
  sudo servctl install myapp --exec /www/wwwroot/myapp/myapp --workdir /www/wwwroot/myapp --user ubuntu
  sudo servctl install myapp --exec ./myapp --user ubuntu -- --port 8200
  servctl status myapp
  servctl list
  sudo servctl logs myapp --follow

NAME 是服务名，不带 .service；--user 必填，必须是现有用户。
--exec 相对路径和默认工作目录均以执行命令时的目录为准。
install 自动启动并设置开机自启；同名服务拒绝覆盖。
程序需可执行且以前台方式运行；不会编译源码或配置端口。
卸载保留业务文件。仅管理 servctl 创建的服务。
list 列出所有托管服务的运行状态和开机自启状态，包含已停止和失败的服务。
写操作需要 sudo；日志权限不足时也请使用 sudo。
`

type Backend interface {
	Install(context.Context, systemd.Config) error
	Action(context.Context, string, string, bool) error
	List(context.Context) error
}

type App struct {
	Backend            Backend
	Out, Err           io.Writer
	Version, GOOS, Cwd string
	EUID               int
	CheckSystem        func() error
}

func New(version string, out, errOut io.Writer) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &App{Backend: systemd.NewManager(out), Out: out, Err: errOut, Version: version, GOOS: runtime.GOOS, EUID: os.Geteuid(), Cwd: cwd, CheckSystem: checkSystem}, nil
}

func checkSystem() error {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return fmt.Errorf("系统未运行 systemd（容器需以 systemd 启动）: %w", err)
	}
	for _, name := range []string{"systemctl", "systemd-analyze", "journalctl"} {
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
	case "install", "start", "stop", "restart", "status", "logs", "uninstall", "list":
	default:
		return fmt.Errorf("未知命令 %q，运行 servctl --help 查看用法", action)
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(a.Out, help)
		return nil
	}
	name := ""
	rest := args[1:]
	if action != "list" {
		if len(args) < 2 {
			return fmt.Errorf("缺少服务名，运行 servctl --help 查看用法")
		}
		name = args[1]
		if err := systemd.ValidateName(name); err != nil {
			return err
		}
		rest = args[2:]
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprint(a.Out, help) }
	c := systemd.Config{Name: name}
	follow := false
	if action == "install" {
		fs.StringVar(&c.Executable, "exec", "", "可执行文件路径（必填）")
		fs.StringVar(&c.WorkDir, "workdir", a.Cwd, "工作目录")
		fs.StringVar(&c.User, "user", "", "现有运行用户（必填）")
		for i, arg := range rest {
			if arg == "--" {
				c.Args = append([]string{}, rest[i+1:]...)
				rest = rest[:i]
				break
			}
		}
	} else if action == "logs" {
		fs.BoolVar(&follow, "follow", false, "持续查看日志，Ctrl-C 退出")
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		if action == "list" {
			return fmt.Errorf("list 不接受服务名或其他参数")
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
	}
	if a.GOOS != "linux" {
		return fmt.Errorf("服务管理仅支持 Linux + systemd，当前系统为 %s", a.GOOS)
	}
	if action != "status" && action != "logs" && action != "list" && a.EUID != 0 {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo servctl %s ...", action)
	}
	if err := a.CheckSystem(); err != nil {
		return err
	}
	if action == "install" {
		return a.Backend.Install(ctx, c)
	}
	if action == "list" {
		return a.Backend.List(ctx)
	}
	return a.Backend.Action(ctx, action, name, follow)
}
