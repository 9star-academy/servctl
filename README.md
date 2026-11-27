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

在 Linux 服务器执行，安装最新正式 Release：

```bash
curl -fsSL https://raw.githubusercontent.com/9star-academy/servctl/main/scripts/install.sh | sudo bash
```

安装脚本识别 CPU 架构，下载最新正式 Release，校验 SHA-256，安装到 `/usr/local/bin/servctl`。失败时返回非零退出码；下载、校验或解压失败不会覆盖已安装的二进制。依赖 `curl`、`tar`、`sha256sum` 及 Ubuntu 常规系统命令。

也可以先下载检查脚本再运行，或指定版本：

```bash
curl -fL https://raw.githubusercontent.com/9star-academy/servctl/main/scripts/install.sh -o /tmp/servctl-install.sh
less /tmp/servctl-install.sh
sudo bash /tmp/servctl-install.sh v0.3.0
```

验证安装：

```bash
servctl --version
servctl --help
servctl list
```

如需自定义工具安装目录，可用 `sudo env SERVCTL_INSTALL_DIR=/opt/bin bash /tmp/servctl-install.sh`；需要自行将该目录加入 PATH。如服务器访问 GitHub 受限，可通过 `SERVCTL_REPO_URL=https://你的镜像地址/9star-academy/servctl` 指定镜像仓库（`install.sh` 与 `servctl update` 均支持，且仍强制 HTTPS 与 SHA-256 校验）。再次运行安装脚本会更新 CLI，不重启或修改已经注册的业务服务。

如果提示“仓库尚未发布正式 Release”（旧脚本提示“无法解析最新 Release”），请先检查 [Releases](https://github.com/9star-academy/servctl/releases)。源码已推送到 `main` 不代表二进制已发布；需要按本文末尾的发布步骤创建正式版本，并确认包含 `servctl_linux_amd64.tar.gz`、`servctl_linux_arm64.tar.gz` 和 `checksums.txt`。如果发布未完成，请检查 [Actions](https://github.com/9star-academy/servctl/actions) 的失败日志；修复后再发布。

### 更新 servctl

默认安装到 `/usr/local/bin` 时，执行以下命令检查并更新 servctl 本身。

```bash
sudo servctl update
servctl --version
```

- 每次执行 `update` 都会检查 GitHub 上的最新正式版。发现更高版本时，自动下载当前 CPU 架构的发布包，通过 SHA-256 校验后替换正在使用的 `servctl` 文件。当前版本已是最新时直接退出。
- 更新期间已托管的业务进程可以继续运行。下载或校验失败时，原有程序仍可使用。
- 自定义安装目录可以直接运行对应路径下的 `servctl update`。需要写入该目录的权限，必要时通过 `sudo` 执行该完整路径。
- 如果当前版本提示 `update` 是未知命令，先按上面的安装命令升级到带 `update` 的正式版。源码构建的 `dev` 版本可通过重新构建或运行安装脚本升级。

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

**默认进程检查不是业务健康检查。** 如需自动验证业务就绪，可在 `install` / `start` / `restart` 时传入 `--wait-port` 或 `--health-url`；未传时安装成功后仍需自行验证端口或健康接口，晚于观察窗口发生的错误请查日志。

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

- `--port` 只是示例，以你应用实际支持的参数为准。参数原样传递，不经过 shell；`$HOME`、`%n` 等不会由 systemd 展开。参数里不要放密码，因为 unit 文件可被本机用户读取。

### 环境变量与常用服务配置

`install` 支持通过参数配置环境变量、资源限制、重启策略和启动顺序依赖：

```bash
sudo servctl install myapp \
  --exec /www/wwwroot/myapp/myapp \
  --workdir /www/wwwroot/myapp \
  --user ubuntu \
  --env APP_ENV=production \
  --env-file /etc/myapp/secrets.env \
  --desc "MyApp API" \
  --restart on-failure \
  --restart-sec 5s \
  --memory-max 512M \
  --cpu-quota 150% \
  --after postgresql.service \
  -- --port 8200
```

- `--env KEY=VALUE`：设置单个环境变量，可重复指定。含密码等敏感信息建议使用 `--env-file`。
- `--env-file PATH`：通过 systemd 原生 `EnvironmentFile=` 加载环境变量文件，可重复指定；文件可设为 `0600 root:root`，由 systemd 在降权前读取，避免敏感信息暴露在 `0644` 的 unit 文件中。
- `--desc`：自定义服务描述（默认 `servctl service <NAME>`）。
- `--restart` / `--restart-sec`：覆盖重启策略（默认 `on-failure`）和重启间隔（默认 `3`）。
- `--memory-max` / `--cpu-quota`：通过 cgroup 限制服务内存上限（如 `512M`、`1G`）和 CPU 配额（如 `50%`、`200%`）。
- `--after`：追加启动顺序依赖（如 `postgresql.service`、`redis.service`），可重复指定。
- `--no-enable` / `--no-start`：按需跳过开机自启或跳过立即启动。
- `--replace`：原地更新已有的 servctl 托管服务（先 `systemd-analyze verify`，原子替换并重启；若启动或就绪检查失败则自动恢复原 unit 配置并重启原服务）。拒绝覆盖非 servctl 托管的系统服务。
- `--wait-port PORT` / `--health-url URL` / `--ready-timeout DUR`（默认 `15s`）：在确认进程 `active` 后继续等待本机 TCP 端口监听或 HTTP(S) 返回 `2xx`，`install` 与 `start`/`restart` 均支持。

原地更新已有托管服务并附带健康检查示例：

```bash
sudo servctl install myapp --replace \
  --exec /www/wwwroot/myapp/myapp \
  --workdir /www/wwwroot/myapp \
  --user ubuntu \
  --env-file /etc/myapp/secrets.env \
  --wait-port 8200 \
  --health-url http://127.0.0.1:8200/health \
  --ready-timeout 20s
```

## 日常管理

| 操作 | 命令 |
| --- | --- |
| 列出所有托管服务 | `servctl list`（支持 `--json`、`--all`、`--failed`） |
| 查看服务配置 | `servctl show myapp` |
| 查看状态 | `servctl status myapp` |
| 查看最近 50 行日志 | `sudo servctl logs myapp` |
| 持续查看日志，Ctrl-C 退出 | `sudo servctl logs myapp -f`（或 `--follow`） |
| 按行数、时间或级别过滤日志 | `sudo servctl logs myapp -n 200 --since "1 hour ago" --level err` |
| 重启服务 | `sudo servctl restart myapp`（可选 `--wait-port` / `--health-url`） |
| 停止服务 | `sudo servctl stop myapp` |
| 启动服务 | `sudo servctl start myapp`（可选 `--wait-port` / `--health-url`） |
| 开启 / 关闭开机自启 | `sudo servctl enable myapp` / `sudo servctl disable myapp` |
| 卸载服务 | `sudo servctl uninstall myapp` |

- `stop` 只停止当前运行，保留开机自启；`start` 不修改自启设置；`enable` / `disable` 仅切换开机自启，不启停当前进程。
- servctl 创建的服务默认使用 `Restart=on-failure`，异常退出后等待 3 秒重试，60 秒内最多启动 5 次（可通过 `--restart` / `--restart-sec` 覆盖）。正常退出、手动停止不会自动重启；触发限流后修复问题，再执行 `sudo systemctl reset-failed myapp` 和 `sudo servctl start myapp`。
- `status` 透传 `systemctl status` 的退出码（例如已停止服务返回 `3`）；查看日志的权限由系统控制，权限不足请加 `sudo`。
- 同名服务默认拒绝覆盖，包括系统已有服务、其他工具创建的服务和被 mask 的服务；修改已有托管服务参数可使用 `servctl install myapp --replace ...` 原地更新。
- `uninstall` 只接受 `/etc/systemd/system/` 中带 servctl 标记的普通服务文件。它会停止并禁用服务、移除 unit，**不会删除程序、配置、日志或数据库**。
- 更新程序可先 `stop`，替换可执行文件后 `start`，或通过 `install --replace` 原地变更配置。不提供零停机更新、自动构建、文件上传或应用版本回滚。
- systemd 不继承你交互式终端的完整环境；如需环境变量请使用 `--env` 或 `--env-file`。

### 操作已有的系统服务

`status`、`show`、`logs`、`start`、`stop`、`restart`、`enable` 和 `disable` 可用于系统包安装或手工创建的 systemd 服务。支持简写、完整的 `.service` 名称以及 `getty@tty1.service` 这样的实例名。

```bash
servctl status nginx.service
servctl show nginx.service
sudo servctl logs nginx -f -n 100
sudo servctl start nginx
sudo servctl stop nginx
sudo servctl restart nginx
sudo servctl enable nginx
sudo servctl disable nginx
```

`logs` 读取 systemd journal。服务自己写入的日志文件需按该服务的配置查看。

### 列出托管服务

服务较多时，运行以下命令查看清单，无需 `sudo`：

```bash
servctl list                # 仅列出 servctl 托管服务
servctl list --failed       # 仅查看异常失败的服务
servctl list --all          # 列出系统全部 .service（含非托管）
servctl list --json         # 输出 JSON（适合脚本/监控采集）
```

示例输出（具体状态以当前系统为准）：

```text
NAME        STATE     AUTOSTART  PID    UPTIME  RESTARTS  USER    MEM
myapp       active    enabled    24817  3d4h    0         ubuntu  84.0MiB
myapp-test  failed    enabled    -      -       5         deploy  -
worker      inactive  disabled   -      -       0         ubuntu  -
```

- 默认只列出 `/etc/systemd/system/` 中带 servctl 创建标记的普通 `.service` 文件，按服务名排序；跳过系统服务、其他工具创建的服务及符号链接（加 `--all` 可查看系统全部非模板服务，JSON 输出中通过 `managed` 字段区分）。
- 包含已停止、启动失败和未设置开机自启的托管服务，不按运行状态筛选。
- `NAME` 为服务名，不带 `.service`；`STATE` 为运行状态，常见值为 `active`（运行）、`inactive`（停止）、`failed`（失败）。
- `AUTOSTART` 为开机自启状态，常见值为 `enabled`（已启用）、`disabled`（未启用）；其他状态保留 [systemd 原始值](https://www.freedesktop.org/software/systemd/man/255/org.freedesktop.systemd1.html)。运行状态与开机自启状态分别展示。
- `PID` 为主进程 ID。没有主进程时显示 `-`。
- `UPTIME` 为本次进入 `active` 后的时长，例如 `42s` 或 `3d4h`。停止或失败时显示 `-`。
- `RESTARTS` 为 systemd 当前记录的自动重启次数。计数可能重置，无值时显示 `-`。
- `USER` 为服务配置的运行账号。无值时显示 `-`。
- `MEM` 为整个服务（含子进程）当前占用的内存。没有可用统计时显示 `-`。
- 没有托管服务时显示“暂无 servctl 创建的服务。”；支持 `--json`（输出结构化 JSON，适合脚本和监控采集）、`--all`（包含系统内非 servctl 托管的服务）以及 `--failed`（仅筛选 `failed` 状态服务）。在交互式终端中按状态自动高亮着色，管道输出或设置 `NO_COLOR=1` 时自动关闭。读取目录、文件或查询状态失败时返回非零退出码。此命令只查询，不启动、停止或修改服务。

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

检查内容包括运行用户、工作目录、特殊字符参数、环境变量（`--env` / `--env-file`）、资源限制、active/enabled、重复安装拒绝、`install --replace` 原地更新、异常退出恢复、启停/重启、`show`/`enable`/`disable`、卸载保留业务文件、启动失败清理、已有服务的运行操作和覆盖/卸载保护，以及 `list`（含 `--json`）在运行、停止和失败状态下的输出及非托管服务过滤。

GitHub CI 在 Ubuntu 24.04 运行上述实机检查。此脚本不重启主机；开机恢复需在可重启测试机上额外验证：注册测试应用 → 重启 → 检查 `is-active` / `is-enabled` / 应用响应。不要用生产服务器执行重启验证。

## 发布到 GitHub

发布流程位于 `.github/workflows/release.yml`。在代码已审核、提交并推送且 CI 通过后，由维护者创建版本 tag：

```bash
git tag v0.3.0
git push origin v0.3.0
```

Release workflow 先执行测试及 systemd 冒烟检查，再构建并发布：

```text
servctl_linux_amd64.tar.gz
servctl_linux_arm64.tar.gz
checksums.txt
```

版本由 tag 注入 `servctl --version`。发布流程会将正式版本设为 latest，并在发布后使用真实下载的安装脚本和 Release 包验证全新安装、从 `v0.1.0` 更新、`list` 可用性，以及更新前后测试服务的配置和进程 PID 保持不变。检查使用临时安装目录和独立命名的测试服务，完成后清理。

只有 Release 和发布后验证都通过，才算完成发布。失败时先查看 Actions 日志；Release 可能已经存在，需要同时确认资产和安装验证结果。

## 开源协议

本项目基于 [MIT License](LICENSE) 开源。

ReedTang666 ♥️ you
