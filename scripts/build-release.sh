#!/usr/bin/env bash
# Build versioned release archives and checksums from a SemVer tag.
set -euo pipefail

release_version="${1:?usage: build-release.sh vMAJOR.MINOR.PATCH [OUTPUT_DIR]}"
output_dir="${2:-dist}"

if [[ ! "$release_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  printf 'invalid release version: %s (expected vMAJOR.MINOR.PATCH without leading zeroes)\n' "$release_version" >&2
  exit 2
fi

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
release_dir="$output_dir/$release_version"
if [[ -e "$release_dir" ]]; then
  printf 'release output already exists: %s\n' "$release_dir" >&2
  exit 1
fi
mkdir -p "$release_dir"

stage_dir="$(mktemp -d)"
trap 'rm -rf "$stage_dir"' EXIT
host_target="$(go env GOOS)/$(go env GOARCH)"

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
  target_os="${target%/*}"
  target_arch="${target#*/}"
  archive_name="agentctl_${release_version}_${target_os}_${target_arch}"
  binary_name=agentctl
  if [[ "$target_os" == windows ]]; then
    binary_name=agentctl.exe
  fi

  rm -rf "$stage_dir/package"
  mkdir -p "$stage_dir/package"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$release_version" \
    -o "$stage_dir/package/$binary_name" ./cmd/agentctl
  if [[ "$target" == "$host_target" ]]; then
    actual_version="$("$stage_dir/package/$binary_name" --version)"
    if [[ "$actual_version" != "agentctl $release_version" ]]; then
      printf 'built binary reported %s, expected agentctl %s\n' "$actual_version" "$release_version" >&2
      exit 1
    fi
  fi

  cp LICENSE README.md "$stage_dir/package/"
  if [[ "$target_os" == windows ]]; then
    (cd "$stage_dir/package" && zip -q "$release_dir/$archive_name.zip" "$binary_name" LICENSE README.md)
  else
    tar -czf "$release_dir/$archive_name.tar.gz" -C "$stage_dir/package" "$binary_name" LICENSE README.md
  fi
done

(cd "$release_dir" && shasum -a 256 ./*.tar.gz ./*.zip > SHA256SUMS)
