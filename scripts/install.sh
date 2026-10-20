#!/usr/bin/env bash
set -euo pipefail

die() { printf '错误: %s\n' "$*" >&2; exit 1; }

[[ $# -le 1 ]] || die '用法: install.sh [vX.Y.Z]'
[[ $(uname -s) == Linux ]] || die '仅支持 Linux 服务器。'
[[ $(id -u) == 0 ]] || die '请使用 sudo bash 执行安装脚本。'
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "不支持的 CPU 架构: $(uname -m)" ;;
esac
for tool in curl tar sha256sum install mktemp awk; do
  command -v "$tool" >/dev/null 2>&1 || die "缺少命令: $tool"
done

repo=https://github.com/9star-academy/servctl
version=${1:-latest}
download() {
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --connect-timeout 15 --max-time 180 --retry 2 "$@"
}
if [[ $version == latest ]]; then
  resolved=$(download -o /dev/null -w '%{url_effective}' "$repo/releases/latest") || die '找不到最新 Release；请确认仓库已发布版本，且服务器能访问 GitHub。'
  if [[ $resolved == "$repo/releases" || $resolved == "$repo/releases/" ]]; then
    die "仓库尚未发布正式 Release；仅上传源码不能在线安装。请先发布包含 servctl_linux_${arch}.tar.gz 和 checksums.txt 的正式版本：$repo/releases"
  fi
  [[ $resolved == "$repo/releases/tag/"* ]] || die '无法解析最新 Release。'
  version=${resolved##*/}
fi
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die '版本号格式必须为 vX.Y.Z。'

target_dir=${SERVCTL_INSTALL_DIR:-/usr/local/bin}
[[ $target_dir == /* ]] || die '安装目录必须为绝对路径。'
target="$target_dir/servctl"
[[ ! -L $target ]] || die "拒绝覆盖符号链接: $target"
[[ ! -e $target || -f $target ]] || die "目标不是普通文件: $target"

tmp=$(mktemp -d)
staged=
cleanup() {
  rm -rf "$tmp"
  if [[ -n $staged ]]; then rm -f "$staged"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

archive="servctl_linux_${arch}.tar.gz"
base="$repo/releases/download/$version"
printf '下载 servctl %s (linux/%s)...\n' "$version" "$arch"
download -o "$tmp/$archive" "$base/$archive" || die '下载失败；请检查版本资产是否完整及 GitHub 网络连通性。'
download -o "$tmp/checksums.txt" "$base/checksums.txt" || die '下载校验文件失败。'
checksum=$(awk -v name="$archive" '$2 == name {print $1}' "$tmp/checksums.txt")
[[ $checksum =~ ^[0-9a-fA-F]{64}$ ]] || die '校验文件缺少唯一有效的 SHA-256 条目。'
printf '%s  %s\n' "$checksum" "$archive" > "$tmp/verify.txt"
(cd "$tmp" && sha256sum -c verify.txt) || die 'SHA-256 校验失败，未安装。'
# Extract only this member to stdout, never archive-controlled filesystem paths.
tar -xzOf "$tmp/$archive" servctl > "$tmp/servctl" || die '解压失败。'
[[ -s $tmp/servctl ]] || die '发布包中的程序为空。'
mkdir -p "$target_dir"
staged=$(mktemp "$target_dir/.servctl-install.XXXXXX")
install -m 0755 "$tmp/servctl" "$staged"
mv -f "$staged" "$target"
staged=
printf '已安装: %s\n版本: %s\n验证: %s --version\n帮助: %s --help\n' "$target" "$version" "$target" "$target"
