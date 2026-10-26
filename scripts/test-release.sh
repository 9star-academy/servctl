#!/usr/bin/env bash
# Live release verification: downloads real assets and creates one disposable service.
set -euo pipefail
[[ $# == 2 ]] || { echo 'Usage: test-release.sh VERSION PREVIOUS_VERSION' >&2; exit 1; }
version=$1
previous=$2
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && $previous =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && $version != "$previous" ]] || {
  echo 'Requires two different vX.Y.Z versions.' >&2; exit 1
}
[[ $(uname -s) == Linux && $(id -u) == 0 && -d /run/systemd/system ]] || {
  echo 'Requires root on a disposable Linux systemd host.' >&2; exit 1
}
root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d /tmp/servctl-release.XXXXXX)
name="servctl-release-${scratch##*.}"
unit="/etc/systemd/system/$name.service"
created=0
cleanup() {
  if [[ $created == 1 ]]; then
    systemctl disable --now "$name.service" >/dev/null 2>&1 || true
    rm -f "$unit"
    systemctl daemon-reload
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT
repo=https://github.com/9star-academy/servctl

# Verify the same latest redirect used by the public one-line installer.
resolved=
for _ in {1..12}; do
  resolved=$(curl -fsSL --connect-timeout 15 --max-time 30 --retry 2 \
    -o /dev/null -w '%{url_effective}' "$repo/releases/latest")
  if [[ $resolved == "$repo/releases/tag/$version" ]]; then break; fi
  sleep 5
done
[[ $resolved == "$repo/releases/tag/$version" ]] || { echo "Latest release is not $version: $resolved" >&2; exit 1; }
curl -fsSL --connect-timeout 15 --max-time 60 --retry 2 \
  https://raw.githubusercontent.com/9star-academy/servctl/main/scripts/install.sh -o "$scratch/install.sh"
cmp "$root/scripts/install.sh" "$scratch/install.sh"

echo "Fresh install from latest (expected $version)..."
env SERVCTL_INSTALL_DIR="$scratch/fresh" bash "$scratch/install.sh"
[[ $("$scratch/fresh/servctl" --version) == "servctl $version" ]]
"$scratch/fresh/servctl" --help > "$scratch/help"
grep -Fq 'servctl list' "$scratch/help"
"$scratch/fresh/servctl" list

echo "Install upgrade baseline $previous and create a service..."
env SERVCTL_INSTALL_DIR="$scratch/upgrade" bash "$scratch/install.sh" "$previous"
[[ $("$scratch/upgrade/servctl" --version) == "servctl $previous" ]]
"$scratch/upgrade/servctl" install "$name" --exec /bin/sleep --workdir /tmp --user "$(id -un 65534)" -- 300
created=1
sha256sum "$unit" > "$scratch/unit-before.sha256"
before_pid=$(systemctl show --property=MainPID --value "$name.service")
[[ $before_pid != 0 ]]

echo "Upgrade $previous to latest (expected $version)..."
env SERVCTL_INSTALL_DIR="$scratch/upgrade" bash "$scratch/install.sh"
[[ $("$scratch/upgrade/servctl" --version) == "servctl $version" ]]
sha256sum -c "$scratch/unit-before.sha256"
[[ $(systemctl show --property=MainPID --value "$name.service") == "$before_pid" ]]
systemctl is-active --quiet "$name.service"
systemctl is-enabled --quiet "$name.service"
"$scratch/upgrade/servctl" list > "$scratch/list"
cat "$scratch/list"
awk -v name="$name" '$1 == name && $2 == "active" && $3 == "enabled" { found = 1 } END { exit !found }' "$scratch/list"

"$scratch/upgrade/servctl" uninstall "$name"
created=0
[[ ! -e $unit ]]
echo "Live release verification passed: fresh $version, $previous -> $version, list works, service configuration and PID unchanged."
