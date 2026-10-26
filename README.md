<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/readme/logo-light.svg">
  <img src="docs/assets/readme/logo-light.svg" alt="servctl" width="324" height="90">
</picture>

<p>一条命令，将已有程序注册为 systemd 服务</p>

<p>
  <img src="docs/assets/readme/go.svg" alt="Go 1.24+" width="90" height="28">
  <img src="docs/assets/readme/linux.svg" alt="Linux" width="76" height="28">
  <img src="docs/assets/readme/systemd.svg" alt="systemd" width="94" height="28">
  <img src="docs/assets/readme/architectures.svg" alt="amd64 / arm64" width="142" height="28">
</p>

<p>
  <a href="#一键安装">一键安装</a> ·
  <a href="#部署应用服务">部署示例</a> ·
  <a href="#日常管理">日常管理</a> ·
  <a href="#开发和验证">开发验证</a>
</p>

<p>自动启动 · 开机自启 · 异常恢复 · journald 日志</p>

</div>

---

一条命令，将已有可执行程序注册为 systemd 服务：创建 unit、启动、开机自启、异常退出自动重启。

Go 编写，无第三方 Go 依赖，不运行额外守护进程。进程由 systemd 管理，日志由 journald 管理，方便快速。

## 支持环境

- Linux + 正在运行的 systemd，面向 Ubuntu 20.04+（systemd 240+）。不支持没有 systemd 的普通 Docker 容器。
- Linux amd64 / arm64 发布包。服务器无需安装 Go。
- 安装工具及修改系统服务需要 `sudo`；业务进程使用 `--user` 指定的已有本地用户。
- 程序应是对应服务器操作系统/CPU 的可执行文件，或带有效 shebang 的可执行脚本，并以前台方式运行。不直接运行 `.go` 源码。

## 一键安装

> [!NOTE]
> 首次发布说明：下面的在线安装命令需要先将代码推送到 GitHub，并发布包含二进制及校验文件的 Release。仅有仓库源码时，安装链接不能完成安装。发布步骤见本文末尾。

首个正式 Release 发布后，在服务器执行：

```bash
curl -fsSL https://raw.githubusercontent.com/9star-academy/servctl/main/scripts/install.sh | sudo bash
```

安装脚本识别 CPU 架构，下载最新正式 Release，校验 SHA-256，安装到 `/usr/local/bin/servctl`。失败时返回非零退出码；下载、校验或解压失败不会覆盖已安装的二进制。依赖 `curl`、`tar`、`sha256sum` 及 Ubuntu 常规系统命令。

也可以先下载检查脚本再运行，或指定版本：

```bash
curl -fL https://raw.githubusercontent.com/9star-academy/servctl/main/scripts/install.sh -o /tmp/servctl-install.sh
less /tmp/servctl-install.sh
sudo bash /tmp/servctl-install.sh v0.1.0
```

验证安装：

```bash
servctl --version
servctl --help
```

如需自定义工具安装目录，可用 `sudo env SERVCTL_INSTALL_DIR=/opt/bin bash /tmp/servctl-install.sh`；需要自行将该目录加入 PATH。再次运行安装脚本会更新 CLI，不重启或修改已经注册的业务服务。

如果提示“仓库尚未发布正式 Release”（旧脚本提示“无法解析最新 Release”），请先检查 [Releases](https://github.com/9star-academy/servctl/releases)。源码已推送到 `main` 不代表二进制已发布；需要按本文末尾的发布步骤创建正式版本，并确认包含 `servctl_linux_amd64.tar.gz`、`servctl_linux_arm64.tar.gz` 和 `checksums.txt`。如果发布未完成，请检查 [Actions](https://github.com/9star-academy/servctl/actions) 的失败日志；修复后再发布。

## 部署应用服务

以下以 `ubuntu` 用户运行 `myapp` 为例。

假设你已上传 **Linux 可执行文件** 到 `/www/wwwroot/myapp/myapp`，配置文件位于该项目目录，程序配置为监听 8200：

```bash
# 上传后若没有执行权限，需要先设置
chmod +x /www/wwwroot/myapp/myapp

sudo servctl install myapp \
  --exec /www/wwwroot/myapp/myapp \
  --workdir /www/wwwroot/myapp \
  --user ubuntu
```

- `myapp` 是自定义服务名，生成 `/etc/systemd/system/myapp.service`，不必与程序文件名相同。
- `--exec` 是实际启动的程序。
- `--workdir` 是读取相对配置/写入相对文件的工作目录，默认当前目录。
- 工作目录支持中间的空格和特殊字符，但最后一级目录名不能以空格结尾；部分 systemd 版本会丢失末尾空格，工具会在创建服务前明确拒绝此类目录。
- `--user ubuntu` 指定业务进程用户，必填。`sudo` 不会让业务程序自动以 root 运行。
- 相对程序路径始终相对你执行 servctl 时的目录解析，而不是相对 `--workdir`。

`ubuntu` 必须有程序及父目录的访问/执行权限、配置文件读取权限，以及业务需要的日志/上传目录写入权限。servctl 不自动改业务文件的所有者或权限。

安装会先检查路径和用户、校验 unit，然后启动、设置开机自启并观察进程两秒。检查失败会打印日志并尝试撤销本次安装；如果无法停止/禁用服务，则保留 unit 并明确提示清理失败，便于人工处理。

**进程检查不是业务健康检查。** 安装成功后仍需验证你的端口或健康接口；晚于观察窗口发生的错误请查日志。

```bash
systemctl is-active myapp       # 预期 active
systemctl is-enabled myapp      # 预期 enabled
sudo ss -ltnp 'sport = :8200'    # 确认监听进程
sudo servctl logs myapp
```

8200 由应用配置决定，servctl 不配置端口、Nginx、DNS、安全组或防火墙，也不会停止已有的端口占用者。

如果应用支持通过参数设置端口，将应用参数写在 `--` 后：

```bash
sudo servctl install myapp \
  --exec /www/wwwroot/myapp/myapp \
  --workdir /www/wwwroot/myapp \
  --user ubuntu \
  -- --port 8200
```

`--port` 只是示例，以你应用实际支持的参数为准。参数原样传递，不经过 shell；`$HOME`、`%n` 等不会由 systemd 展开。参数里不要放密码，因为 unit 文件可被本机用户读取。

## 日常管理

| 操作 | 命令 |
| --- | --- |
| 列出所有托管服务 | `servctl list` |
| 查看状态 | `servctl status myapp` |
| 查看最近 50 行日志 | `sudo servctl logs myapp` |
| 持续查看日志，Ctrl-C 退出 | `sudo servctl logs myapp --follow` |
| 重启服务 | `sudo servctl restart myapp` |
| 停止服务 | `sudo servctl stop myapp` |
| 启动服务 | `sudo servctl start myapp` |
| 卸载服务 | `sudo servctl uninstall myapp` |

- `stop` 只停止当前运行，保留开机自启；`start` 不修改自启设置。
- `Restart=on-failure`：异常退出后等待 3 秒重试，60 秒内最多启动 5 次。正常退出、手动停止不会自动重启；触发限流后修复问题，再执行 `sudo systemctl reset-failed myapp` 和 `sudo servctl start myapp`。
- `status` 对停止/失败的服务保留非零退出码；查看日志的权限由系统控制，权限不足请加 `sudo`。
- 同名服务一律拒绝覆盖，包括系统已有服务、其他工具创建的服务和被 mask 的服务。
- 管理命令只接受 `/etc/systemd/system/` 中带 servctl 标记的普通 unit 文件，拒绝符号链接和非托管服务。
- `uninstall` 停止并禁用服务、移除 unit，**不会删除程序、配置、日志或数据库**。
- 更新程序可先 `stop`，替换可执行文件后 `start`。第一版不提供零停机更新、自动构建、文件上传或应用版本回滚。更改安装参数需先卸载再重新注册，会有停机。
- systemd 不继承你交互式终端的完整环境；第一版不提供环境变量配置参数，使用应用已有配置文件。

### 列出托管服务

服务较多时，运行以下命令查看清单，无需 `sudo`：

```bash
servctl list
```

示例输出（具体状态以当前系统为准）：

```text
NAME        STATE     AUTOSTART
myapp       active    enabled
myapp-test  failed    enabled
worker      inactive  disabled
```

- 只列出 `/etc/systemd/system/` 中带 servctl 创建标记的普通 `.service` 文件，按服务名排序；跳过系统服务、其他工具创建的服务及符号链接。
- 包含已停止、启动失败和未设置开机自启的托管服务，不按运行状态筛选。
- `NAME` 为服务名，不带 `.service`；`STATE` 为运行状态，常见值为 `active`（运行）、`inactive`（停止）、`failed`（失败）。
- `AUTOSTART` 为开机自启状态，常见值为 `enabled`（已启用）、`disabled`（未启用）；其他状态保留 [systemd 原始值](https://www.freedesktop.org/software/systemd/man/255/org.freedesktop.systemd1.html)。运行状态与开机自启状态分别展示。
- 没有托管服务时显示“暂无 servctl 创建的服务。”；读取目录、文件或查询状态失败时返回非零退出码。此命令只查询，不启动、停止或修改服务。

## 开发和验证

需要 Go 1.24+：

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go vet ./...
bash scripts/test-install.sh
go build -o servctl ./cmd/servctl
./servctl --help
```

macOS 可以开发、运行单元测试和查看帮助；服务管理只在 Linux 上执行。

交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/servctl-linux-amd64 ./cmd/servctl
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/servctl-linux-arm64 ./cmd/servctl
```

在**一次性 Linux 测试机**运行完整 systemd 冒烟检查（会创建/删除独立命名的测试服务）：

```bash
go build -o /tmp/servctl ./cmd/servctl
sudo bash scripts/test-systemd.sh /tmp/servctl
```

检查内容包括运行用户、工作目录、特殊字符参数、active/enabled、重复安装拒绝、异常退出恢复、启停/重启、卸载保留业务文件、启动失败清理、非托管服务保护，以及 `list` 的运行/停止/失败状态、自启状态和非托管服务过滤。

GitHub CI 在 Ubuntu 24.04 运行上述实机检查。此脚本不重启主机；开机恢复需在可重启测试机上额外验证：注册测试应用 → 重启 → 检查 `is-active` / `is-enabled` / 应用响应。不要用生产服务器执行重启验证。

## 发布到 GitHub

发布流程位于 `.github/workflows/release.yml`。在代码已审核、提交并推送且 CI 通过后，由维护者创建版本 tag：

```bash
git tag v0.1.0
git push origin v0.1.0
```

Release workflow 先执行测试及 systemd 冒烟检查，再构建并发布：

```text
servctl_linux_amd64.tar.gz
servctl_linux_arm64.tar.gz
checksums.txt
```

版本由 tag 注入 `servctl --version`。待 GitHub Release 成功且 `main` 分支包含安装脚本后，一键安装链接即可使用。Release 失败时先查看 Actions 日志，不要把本地构建通过视为已经发布。

ReedTang666 ♥️ you
