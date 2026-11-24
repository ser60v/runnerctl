#!/bin/sh
# Run with: curl -fsS https://runners.retardsoftware.com/downloads/runnerctl/install.sh | sh
set -eu
umask 077

fail() { printf 'runnerctl installer: %s\n' "$*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v tar >/dev/null 2>&1 || fail 'tar is required'
command -v head >/dev/null 2>&1 || fail 'head is required'
command -v mktemp >/dev/null 2>&1 || fail 'mktemp is required'
if command -v sha256sum >/dev/null 2>&1; then
  sha256_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail 'sha256sum or shasum is required'
fi

origin=https://runners.retardsoftware.com
curl_security="--proto =https --tlsv1.2"
if [ "${RUNNERCTL_INSTALL_TEST_MODE:-}" = 1 ]; then
  printf '%s\n' "${RUNNERCTL_INSTALL_TEST_BASE_URL:-}" | LC_ALL=C grep -Eq '^http://(127\.0\.0\.1|localhost):[0-9]{1,5}$' || fail 'test origin must be loopback HTTP'
  origin=$RUNNERCTL_INSTALL_TEST_BASE_URL
  curl_security=
elif [ -n "${RUNNERCTL_INSTALL_TEST_BASE_URL:-}" ]; then
  fail 'test origin requires RUNNERCTL_INSTALL_TEST_MODE=1'
fi

os=$(uname -s)
arch=$(uname -m)
if [ "${RUNNERCTL_INSTALL_TEST_MODE:-}" = 1 ]; then
  os=${RUNNERCTL_INSTALL_TEST_OS:-$os}
  arch=${RUNNERCTL_INSTALL_TEST_ARCH:-$arch}
fi
case "$os" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail "unsupported operating system: $os" ;; esac
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail "unsupported architecture: $arch" ;; esac

temp_root=${TMPDIR:-/tmp}
tmp=$(mktemp -d "$temp_root/runnerctl-install.XXXXXXXX") || fail 'cannot create temporary directory'
trap 'rm -rf "$tmp"' 0
trap 'exit 1' 1 2 3 15
download() {
  # No redirects: assets and checksums must stay on the fixed origin.
  curl --fail --silent --show-error --connect-timeout 10 --max-time 120 \
    --max-filesize 67108864 $curl_security --output "$2" "$origin$1" || fail 'download failed'
}

download /downloads/runnerctl/latest/VERSION "$tmp/VERSION"
version=$(cat "$tmp/VERSION")
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'invalid release version'
[ "$(wc -l < "$tmp/VERSION" | tr -d ' ')" = 1 ] || fail 'invalid release version file'
archive="runnerctl_${version}_${os}_${arch}.tar.gz"
base="/downloads/runnerctl/$version"
download "$base/SHA256SUMS" "$tmp/SHA256SUMS"
download "$base/$archive" "$tmp/$archive"
expected=$(awk -v name="$archive" '$2 == name || $2 == "./" name { if (NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-f]+$/) exit 2; found++; digest=$1 } END { if (found != 1) exit 2; print digest }' "$tmp/SHA256SUMS") || fail 'invalid checksum entry'
actual=$(sha256_file "$tmp/$archive")
[ "$actual" = "$expected" ] || fail 'archive checksum mismatch'

# Inspect all members before extracting only the binary to stdout. This rejects
# links, paths, duplicate names and unexpected files, including traversal paths.
members=$(tar -tzf "$tmp/$archive") || fail 'invalid archive'
expected_members=$(printf 'LICENSE\nREADME.md\nSECURITY.md\nVERSION\nrunnerctl')
[ "$members" = "$expected_members" ] || fail 'unsafe archive members'
tar -tvzf "$tmp/$archive" | awk 'substr($1,1,1) != "-" { bad=1 } END { if (NR != 5 || bad) exit 1 }' || fail 'archive contains a non-file entry'

: "${HOME:?HOME is required}"
local_dir="$HOME/.local"
install_dir="$local_dir/bin"
[ ! -L "$local_dir" ] || fail 'installation parent is a symlink'
[ ! -L "$install_dir" ] || fail 'installation directory is a symlink'
mkdir -p "$install_dir" || fail 'cannot create installation directory'
[ -d "$install_dir" ] || fail 'installation path is not a directory'
for directory in "$HOME" "$local_dir" "$install_dir"; do
  [ ! -L "$directory" ] || fail 'installation path contains a symlink'
  unsafe=$(find "$directory" -prune \( ! -user "$(id -un)" -o -perm -020 -o -perm -002 \) -print) || fail 'cannot inspect installation directory'
  [ -z "$unsafe" ] || fail 'installation directory must be owned by the current user and not writable by others'
done
target="$install_dir/runnerctl"
[ ! -L "$target" ] || fail 'existing runnerctl is a symlink'
if [ -e "$target" ]; then [ -f "$target" ] || fail 'existing runnerctl is not a regular file'; fi
staged=$(mktemp "$install_dir/.runnerctl.XXXXXXXX") || fail 'cannot stage binary'
trap 'rm -f "$staged"; rm -rf "$tmp"' 0
(set +e; tar -xOzf "$tmp/$archive" runnerctl; printf '%s\n' "$?" > "$tmp/extract-status") | head -c 67108865 > "$staged" || fail 'cannot extract binary'
[ "$(wc -c < "$staged" | tr -d ' ')" -le 67108864 ] || fail 'binary exceeds size limit'
[ -s "$staged" ] || fail 'empty binary'
extract_status=$(cat "$tmp/extract-status") || fail 'cannot read extraction status'
[ "$extract_status" = 0 ] || fail 'cannot extract binary'
chmod 755 "$staged" || fail 'cannot set binary permissions'
mv -f "$staged" "$target" || fail 'cannot replace binary'
printf 'Installed runnerctl %s at %s\n' "$version" "$target"
case ":$PATH:" in *":$install_dir:"*) ;; *) printf 'Add it to PATH in your shell profile: export PATH="%s:$PATH"\n' "$install_dir" ;; esac
