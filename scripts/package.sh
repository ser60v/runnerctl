#!/bin/sh
set -eu
sh scripts/ensure-env.sh
sh scripts/check-repository.sh
version=$(cat VERSION)
epoch=$(git log -1 --format=%ct)
release_dir=${BUILD_DIR:-.build}/release
package_dir=${BUILD_DIR:-.build}/package
mkdir -p "$release_dir" "$package_dir"
rm -f "$release_dir"/runnerctl_*.tar.gz "$release_dir"/runnerctl_*.zip "$release_dir"/SHA256SUMS
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  binary=runnerctl
  if [ "$target_os" = windows ]; then binary=runnerctl.exe; fi
  rm -f "$package_dir/runnerctl" "$package_dir/runnerctl.exe"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.version=$version" -o "$package_dir/$binary" ./cmd/runnerctl
  cp LICENSE README.md SECURITY.md VERSION "$package_dir/"
  if [ "$target_os" = windows ]; then
    go run ./tools/ziparchive "$package_dir" "$release_dir/runnerctl_${version}_${target_os}_${target_arch}.zip" "$epoch" LICENSE README.md SECURITY.md VERSION "$binary"
  else
    tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
      -C "$package_dir" -cf - LICENSE README.md SECURITY.md VERSION "$binary" | gzip -n > "$release_dir/runnerctl_${version}_${target_os}_${target_arch}.tar.gz"
  fi
done
cp scripts/install.sh scripts/install.ps1 "$release_dir/"
(cd "$release_dir" && sha256sum ./*.tar.gz ./*.zip ./install.sh ./install.ps1 > SHA256SUMS)
rm -rf "$package_dir"
