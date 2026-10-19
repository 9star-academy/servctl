#!/usr/bin/env bash
# Offline installer regression tests: never writes to /usr/local/bin or downloads.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin" "$fixture/release" "$fixture/target" "$fixture/package"
export SERVCTL_FIXTURE="$fixture" SERVCTL_INSTALL_DIR="$fixture/target"
printf '#!/bin/sh\nprintf "servctl v0.1.0\\n"\n' > "$fixture/package/servctl"
tar -czf "$fixture/release/servctl_linux_amd64.tar.gz" -C "$fixture/package" servctl
cp "$fixture/release/servctl_linux_amd64.tar.gz" "$fixture/release/servctl_linux_arm64.tar.gz"
if command -v sha256sum >/dev/null; then
  hasher=$(command -v sha256sum)
  # shellcheck disable=SC2016 # Generate literal arguments for the child script.
  printf '#!/bin/sh\nexec "%s" "$@"\n' "$hasher" > "$fixture/bin/sha256sum"
else
  # shellcheck disable=SC2016 # Generate literal arguments for the child script.
  printf '#!/bin/sh\nexec shasum -a 256 "$@"\n' > "$fixture/bin/sha256sum"
fi
cat > "$fixture/bin/id" <<'EOF'
#!/bin/sh
echo "${TEST_UID:-0}"
EOF
cat > "$fixture/bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) echo "${TEST_OS:-Linux}";;
  -m) echo "${TEST_ARCH:-x86_64}";;
esac
EOF
cat > "$fixture/bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
 case "$1" in
  -o|--output) output=$2; shift 2;;
  -w|--write-out|--proto|--tlsv1.2|--connect-timeout|--max-time|--retry)
   if [ "$1" = --tlsv1.2 ]; then shift; else shift 2; fi;;
  https://*) url=$1; shift;;
  *) shift;;
 esac
done
[ "${TEST_DOWNLOAD_FAIL:-0}" = 0 ] || exit 22
case "$url" in
 https://github.com/9star-academy/servctl/releases/latest)
  printf 'https://github.com/9star-academy/servctl/releases/tag/v0.1.0';;
 https://github.com/9star-academy/servctl/releases/download/v0.1.0/*)
  cp "$SERVCTL_FIXTURE/release/${url##*/}" "$output";;
 *) echo "Unexpected download: $url" >&2; exit 1;;
esac
EOF
chmod +x "$fixture/bin/"*
export PATH="$fixture/bin:$PATH"
make_checksums() {
  (cd "$fixture/release" && sha256sum servctl_linux_*.tar.gz > checksums.txt)
}
expect_fail() {
  if bash "$root/scripts/install.sh" "$@" > "$fixture/output" 2>&1; then
    cat "$fixture/output"
    echo 'Expected installer failure' >&2
    exit 1
  fi
  [[ $(cat "$fixture/target/servctl") == 'keep-existing' ]]
}
make_checksums
bash "$root/scripts/install.sh"
[[ $("$fixture/target/servctl" --version) == 'servctl v0.1.0' ]]
TEST_ARCH=aarch64 bash "$root/scripts/install.sh" v0.1.0
[[ $("$fixture/target/servctl" --version) == 'servctl v0.1.0' ]]
printf 'keep-existing' > "$fixture/target/servctl"
TEST_OS=Darwin expect_fail
TEST_ARCH=riscv64 expect_fail
TEST_UID=1000 expect_fail
TEST_DOWNLOAD_FAIL=1 expect_fail
expect_fail '../../invalid'
expect_fail v0.1.0 extra
printf 'corrupt' >> "$fixture/release/servctl_linux_amd64.tar.gz"
expect_fail v0.1.0
make_checksums
printf 'not-an-archive' > "$fixture/release/servctl_linux_amd64.tar.gz"
make_checksums
expect_fail v0.1.0
echo 'Installer regression tests passed.'
