#!/bin/sh
set -eu

release_version=$(cat VERSION)
printf '%s' "$release_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'
[ -n "${CI_REGISTRY:-}" ]
[ -n "${CI_REGISTRY_USER:-}" ]
[ -n "${CI_REGISTRY_PASSWORD:-}" ]
[ -n "${CI_DEPENDENCY_PROXY_SERVER:-}" ]
[ -n "${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX:-}" ]
[ -n "${CI_DEPENDENCY_PROXY_USER:-}" ]
[ -n "${CI_DEPENDENCY_PROXY_PASSWORD:-}" ]

go_build_image=$(sed -n 's/^ARG GO_BUILD_IMAGE=//p' Dockerfile)
go_build_image_suffix=${go_build_image#docker.io/library/}
[ -n "$go_build_image_suffix" ]
[ "$go_build_image_suffix" != "$go_build_image" ]
proxy_go_build_image="${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/${go_build_image_suffix}"

auth_directory=$(mktemp -d /tmp/runnerctl-registry-auth.XXXXXX)
cleanup() {
  rm -rf -- "$auth_directory"
}
trap cleanup EXIT HUP INT TERM
chmod 700 "$auth_directory"

registry_auth=$(
  printf '%s:%s' "$CI_REGISTRY_USER" "$CI_REGISTRY_PASSWORD" |
    base64 |
    tr -d '\n'
)
proxy_auth=$(
  printf '%s:%s' "$CI_DEPENDENCY_PROXY_USER" "$CI_DEPENDENCY_PROXY_PASSWORD" |
    base64 |
    tr -d '\n'
)
printf '{"auths":{"%s":{"auth":"%s"},"%s":{"auth":"%s"}}}\n' \
  "$CI_REGISTRY" "$registry_auth" \
  "$CI_DEPENDENCY_PROXY_SERVER" "$proxy_auth" >"$auth_directory/config.json"
chmod 600 "$auth_directory/config.json"
export DOCKER_CONFIG=$auth_directory

mkdir -p .buildkit-output
cache_reference="${CI_REGISTRY_IMAGE}:buildcache"
buildctl-daemonless.sh build \
  --frontend dockerfile.v0 \
  --local context=. \
  --local dockerfile=. \
  --opt filename=Dockerfile \
  --opt platform=linux/amd64 \
  --opt "build-arg:APP_VERSION=$release_version" \
  --opt "build-arg:GO_BUILD_IMAGE=$proxy_go_build_image" \
  --import-cache "type=registry,ref=$cache_reference" \
  --export-cache "type=registry,ref=$cache_reference,mode=max,image-manifest=true,oci-mediatypes=true,ignore-error=true" \
  --metadata-file .buildkit-output/metadata.json \
  --output "type=image,name=${CI_REGISTRY_IMAGE}:${release_version},push=true"
[ -s .buildkit-output/metadata.json ]
