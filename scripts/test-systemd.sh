#!/usr/bin/env bash
# Run only on a disposable Linux systemd host; creates/removes uniquely named services.
set -euo pipefail
[[ $(id -u) == 0 && -d /run/systemd/system ]] || { echo 'Requires root on a systemd host.' >&2; exit 1; }
binary=$(readlink -f "${1:?Usage: test-systemd.sh /path/to/servctl}")
scratch=$(mktemp -d /tmp/servctl-smoke.XXXXXX)
name="servctl-smoke-$$"
bad="$name-bad"
foreign="$name-foreign"
cleanup() {
  for service in "$name" "$bad" "$foreign"; do
    systemctl disable --now "$service.service" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/$service.service"
    systemctl reset-failed "$service.service" >/dev/null 2>&1 || true
  done
  systemctl daemon-reload
  rm -rf "$scratch"
}
trap cleanup EXIT
chmod 0755 "$scratch"
run_user=$(id -un 65534)
work="$scratch/app % \$ space "
mkdir "$work"
chown "$run_user" "$work"
program="$work/my % \$ executable"
cat > "$program" <<'EOF'
#!/bin/sh
set -eu
id -un > identity
pwd > cwd
printf '%s\0' "$@" > args
echo 'servctl smoke application started'
exec sleep 300
EOF
chmod 0755 "$program"
# shellcheck disable=SC2016 # The test must pass dollar expressions literally.
args=('' 'space here' '$HOME' '${HOME}' '%n' '"quoted"' '\backslash' ';' $'line\nbreak')
"$binary" install "$name" --exec "$program" --workdir "$work" --user "$run_user" -- "${args[@]}"
systemctl is-active --quiet "$name.service"
systemctl is-enabled --quiet "$name.service"
[[ $(cat "$work/identity") == "$run_user" ]]
[[ $(cat "$work/cwd") == "$work" ]]
printf '%s\0' "${args[@]}" > "$scratch/expected"
cmp "$scratch/expected" "$work/args"
"$binary" status "$name"
"$binary" logs "$name"
if "$binary" install "$name" --exec "$program" --workdir "$work" --user "$run_user"; then
  echo 'Overwrote an existing unit' >&2; exit 1
fi
# Crash recovery must change the process PID without a manual start.
old_pid=$(systemctl show --property=MainPID --value "$name.service")
systemctl kill --kill-who=main --signal=KILL "$name.service"
recovered=0
for _ in {1..15}; do
  new_pid=$(systemctl show --property=MainPID --value "$name.service")
  if [[ $new_pid != 0 && $new_pid != "$old_pid" ]] && systemctl is-active --quiet "$name.service"; then
    recovered=1; break
  fi
  sleep 1
done
[[ $recovered == 1 ]]
"$binary" restart "$name"
"$binary" stop "$name"
if systemctl is-active --quiet "$name.service"; then exit 1; fi
"$binary" start "$name"
"$binary" uninstall "$name"
[[ ! -e /etc/systemd/system/$name.service && -f $program ]]
if systemctl is-enabled --quiet "$name.service"; then exit 1; fi

# A startup failure is not success and must not leave a boot-enabled unit.
if "$binary" install "$bad" --exec /bin/false --workdir "$scratch" --user "$run_user"; then
  echo 'Accepted failed startup' >&2; exit 1
fi
[[ ! -e /etc/systemd/system/$bad.service ]]
if systemctl is-enabled --quiet "$bad.service"; then exit 1; fi

printf '[Service]\nExecStart=/bin/sleep 300\n' > "/etc/systemd/system/$foreign.service"
systemctl daemon-reload
if "$binary" uninstall "$foreign"; then echo 'Removed unmanaged service' >&2; exit 1; fi
[[ -f /etc/systemd/system/$foreign.service ]]
echo 'Systemd smoke tests passed (reboot not tested).'
