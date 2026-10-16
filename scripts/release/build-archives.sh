#!/usr/bin/env bash
set -euo pipefail
: "${VERSION:?}" "${COMMIT:?}" "${BUILD_DATE:?}"
root=$(pwd)
archive_script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/archive.py"
output="$root/dist/release"
mkdir -p "$output"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os=${target%/*}; arch=${target#*/}; ext=''
  [[ "$os" != windows ]] || ext='.exe'
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT
  (cd backend && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -tags embed -trimpath \
    -ldflags="-s -w -X main.Version=$VERSION -X main.Commit=$COMMIT -X main.Date=$BUILD_DATE -X main.BuildType=release" \
    -o "$stage/sub4api$ext" ./cmd/server)
  # Older update clients expect both the old asset and executable names.
  cp "$stage/sub4api$ext" "$stage/sub2api$ext"
  # Package deployment templates only, never a local .env, config.yaml or data volume.
  mkdir -p "$stage/deploy"
  for file in deploy/*.sh deploy/*.md deploy/*.service deploy/docker-compose*.yml deploy/config.example.yaml deploy/.env.example; do
    [[ ! -f "$file" ]] || cp "$file" "$stage/deploy/"
  done
  cp -r backend/resources "$stage/resources"
  cp LICENSE* README* "$stage/"
  for name in sub4api sub2api; do
    archive="${name}_${VERSION}_${os}_${arch}"
    if [[ "$os" == windows ]]; then
      python3 "$archive_script" "$stage" "$output/$archive.zip"
    else
      python3 "$archive_script" "$stage" "$output/$archive.tar.gz"
    fi
  done
  rm -rf "$stage"
  trap - EXIT
done
python3 "$archive_script" --checksums "$output"
