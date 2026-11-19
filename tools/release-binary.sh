#!/usr/bin/env bash
# Build the Linux binary that accompanies a source release.
set -euo pipefail
tag=${1:?usage: tools/release-binary.sh vMAJOR.MINOR.PATCH}
[[ $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "Expected a stable release tag" >&2; exit 1; }
cd "$(git rev-parse --show-toplevel)"
revision=$(git rev-parse --verify "refs/tags/$tag^{commit}")
if [[ $revision != "$(git rev-parse HEAD)" || -n "$(git status --porcelain)" ]]; then
    echo "Check out the clean source tree for $tag" >&2
    exit 1
fi
buildinfo=github.com/openhoo/hoovda/internal/buildinfo
date=$(git show -s --format=%cI "$revision")
mkdir -p "dist/$tag"
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOWORK=off GOFLAGS=
go build -trimpath -buildvcs=false -mod=readonly \
    -ldflags="-s -w -X $buildinfo.Version=${tag#v} -X $buildinfo.Commit=$revision -X $buildinfo.BuildDate=$date" \
    -o "dist/$tag/hoovda-linux-amd64" ./cmd/hoovda
(cd "dist/$tag" && sha256sum hoovda-linux-amd64 > SHA256SUMS)
