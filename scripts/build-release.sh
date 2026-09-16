#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
commit="${2:-}"
output_directory="${3:-dist}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vX.Y.Z COMMIT [OUTPUT_DIRECTORY]" >&2
  exit 1
fi
if [[ ! "$commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "COMMIT must be a full 40-character Git commit SHA" >&2
  exit 1
fi

mkdir -p "$output_directory"
if find "$output_directory" -mindepth 1 -print -quit | grep -q .; then
  echo "output directory must be empty: $output_directory" >&2
  exit 1
fi

temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT
release_version="${version#v}"
source_date_epoch="$(git show -s --format=%ct "$commit")"

for architecture in amd64 arm64; do
  package_name="docker-log-viewer_${release_version}_linux_${architecture}"
  package_directory="$temporary_directory/$package_name"
  mkdir -p "$package_directory"

  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath \
    -ldflags="-s -w -X main.buildVersion=$version -X main.buildCommit=$commit" \
    -o "$package_directory/docker-log-viewer" ./cmd/docker-log-viewer
  cp LICENSE README.md "$package_directory/"

  tar --sort=name --mtime="@$source_date_epoch" --owner=0 --group=0 \
    --numeric-owner -C "$temporary_directory" -czf \
    "$output_directory/$package_name.tar.gz" "$package_name"
done

cp compose.yaml "$output_directory/compose.yaml"
(
  cd "$output_directory"
  sha256sum ./*.tar.gz compose.yaml > checksums.txt
)

expected="docker-log-viewer $version (commit $commit)"
actual="$($temporary_directory/docker-log-viewer_${release_version}_linux_amd64/docker-log-viewer --version)"
if [[ "$actual" != "$expected" ]]; then
  echo "unexpected version output: $actual" >&2
  exit 1
fi

echo "built release assets in $output_directory"
