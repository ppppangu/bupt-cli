#!/bin/sh
set -eu

if [ "$#" -gt 1 ]; then
    printf '%s\n' 'Usage: VERSION=1.0.0 sh scripts/build.sh [OUTPUT_DIRECTORY]' >&2
    exit 2
fi

build_script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
build_project_dir=$(CDPATH= cd "$build_script_dir/.." && pwd)
build_version=${VERSION:-$(cat "$build_project_dir/VERSION")}
build_go=${GO:-go}

case "$build_version" in
    ''|*[!A-Za-z0-9._+-]*)
        printf '%s\n' 'VERSION must contain only ASCII letters, digits, dot, underscore, plus, or hyphen.' >&2
        exit 2
        ;;
esac

build_output=${1:-"$build_project_dir/dist"}
mkdir -p "$build_output"
build_output=$(CDPATH= cd "$build_output" && pwd)
build_stage=$(mktemp -d "$build_output/.bupt-build.XXXXXXXX")
trap 'rm -rf "$build_stage"' 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$build_project_dir"

for build_target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
    build_os=${build_target%/*}
    build_arch=${build_target#*/}
    build_name="bupt-$build_os-$build_arch"
    printf 'Building %s (%s)\n' "$build_name" "$build_version" >&2
    CGO_ENABLED=0 GOOS="$build_os" GOARCH="$build_arch" \
        "$build_go" build -trimpath -buildvcs=false \
        -ldflags "-s -w -X main.version=$build_version" \
        -o "$build_stage/$build_name" ./cmd/bupt
done

(
    cd "$build_stage"
    for build_name in bupt-darwin-amd64 bupt-darwin-arm64 bupt-linux-amd64 bupt-linux-arm64; do
        if command -v sha256sum >/dev/null 2>&1; then
            sha256sum "$build_name"
        elif command -v shasum >/dev/null 2>&1; then
            shasum -a 256 "$build_name"
        else
            printf '%s\n' 'A SHA-256 tool is required: sha256sum or shasum.' >&2
            exit 1
        fi
    done
) > "$build_stage/SHA256SUMS"

for build_name in bupt-darwin-amd64 bupt-darwin-arm64 bupt-linux-amd64 bupt-linux-arm64 SHA256SUMS; do
    mv "$build_stage/$build_name" "$build_output/$build_name"
done

printf '%s\n' "$build_output/SHA256SUMS"
