#!/bin/sh

# With curl | sh, start only after the whole function has arrived.
install_bupt() (
set -eu

release_url=''
release_sums=''
# @online-release@

usage() {
    printf '%s\n' '用法  sh install.sh [--bin-dir 目录]' '默认安装到 ~/.local/bin。'
}

case ${1-} in
    -h|--help) usage; exit 0 ;;
esac
case $# in
    0) bin_dir=${HOME:?}/.local/bin ;;
    2)
        if [ "$1" != "--bin-dir" ] || [ -z "$2" ]; then usage >&2; exit 2; fi
        bin_dir=$2 ;;
    *) usage >&2; exit 2 ;;
esac
case $bin_dir in /*) ;; *) bin_dir=$PWD/$bin_dir ;; esac

case $(uname -s) in
    Darwin) os_name=darwin ;;
    Linux)  os_name=linux ;;
    *) printf '%s\n' '支持 macOS 和 Linux。Windows 用户请在 WSL2 的 Linux 终端中运行。' >&2; exit 1 ;;
esac
case $(uname -m) in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64)  arch=amd64 ;;
    *) printf '%s\n' '此安装包支持 arm64 和 x86_64 处理器。' >&2; exit 1 ;;
esac

program="bupt-$os_name-$arch"
member="bin/$program"
if [ -z "$release_url" ]; then
    bundle_dir=$(CDPATH='' cd -P "$(dirname "$0")" && pwd -P)
    if [ ! -f "$bundle_dir/$member" ] || [ -L "$bundle_dir/$member" ]; then
        printf '%s\n' '安装包缺少当前平台的程序，请重新解压。' >&2
        exit 1
    fi
    release_sums=$(cat "$bundle_dir/SHA256SUMS")
fi
expected=$(printf '%s\n' "$release_sums" | awk -v member="$member" '$2 == member && length($1) == 64 && $1 ~ /^[0-9a-f]+$/ { print $1; count++ } END { if (count != 1) exit 1 }') || {
    printf '%s\n' '缺少当前平台的唯一 SHA-256 校验值。' >&2
    exit 1
}
if command -v sha256sum >/dev/null 2>&1; then
    hash_file() { sha256sum < "$1"; }
elif command -v shasum >/dev/null 2>&1; then
    hash_file() { shasum -a 256 < "$1"; }
else
    printf '%s\n' '需要 sha256sum 或 shasum 来校验程序。' >&2
    exit 1
fi

mkdir -p "$bin_dir"
if [ -d "$bin_dir/bupt" ]; then
    printf '安装目标 %s 是一个目录。\n' "$bin_dir/bupt" >&2
    exit 1
fi
stage=$(mktemp "$bin_dir/.bupt-install.XXXXXXXX")
trap 'rm -f "$stage"' 0
trap 'exit 1' HUP INT TERM
if [ -n "$release_url" ]; then
    curl --proto '=https' --proto-redir '=https' -fsSL --retry 2 -o "$stage" "$release_url/$program"
else
    cp "$bundle_dir/$member" "$stage"
fi
actual=$(hash_file "$stage")
if [ "${actual%% *}" != "$expected" ]; then
    printf '%s\n' '程序校验失败，请从分发者处重新获取安装包。' >&2
    exit 1
fi
chmod 755 "$stage"
mv -f "$stage" "$bin_dir/bupt"
printf '已通过 SHA-256 校验并安装到 %s\n' "$bin_dir/bupt"
case ":${PATH-}:" in
    *":$bin_dir:"*) ;;
    *)
        if [ "$bin_dir" = "${HOME-}/.local/bin" ]; then
            printf '%s\n' '在当前终端执行以下命令以启用 bupt' 'export PATH="$HOME/.local/bin:$PATH"'
        else
            printf '请将 %s 加入 PATH，或使用程序的完整路径。\n' "$bin_dir"
        fi ;;
esac
printf '%s\n' '首次使用运行 bupt init，再运行 bupt login。AI agent 使用 bupt login --agent。'
)
install_bupt "$@"
