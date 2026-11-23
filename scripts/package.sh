#!/bin/sh
set -eu
sh scripts/ensure-env.sh
version=$(cat VERSION)
sh scripts/check-repository.sh
mkdir -p .build/release .build/package
epoch=$(git log -1 --format=%ct)
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.version=$version" -o .build/package/runnerctl ./cmd/runnerctl
  cp README.md VERSION .build/package/
  tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
    -C .build/package -cf - README.md VERSION runnerctl | gzip -n > ".build/release/runnerctl_${version}_${target_os}_${target_arch}.tar.gz"
done
(cd .build/release && sha256sum ./*.tar.gz > SHA256SUMS)
rm -rf .build/package
