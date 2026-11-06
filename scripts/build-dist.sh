#!/usr/bin/env bash
# 三平台发布产物 + SHA256SUMS（先例 code/tools/ssh-tunnel-ui/scripts/build-dist.sh）。
# 本机（Windows）只能交叉编译出 Linux/macOS 二进制；.app 打包请在 macOS 上跑。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.1.0}"
OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

export CGO_ENABLED=0

echo "==> Windows amd64（GUI 子系统，双击无控制台窗口）"
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H windowsgui" -o "$OUT/archify-view-windows-amd64.exe" .

echo "==> Linux amd64"
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$OUT/archify-view-linux-amd64" .

echo "==> macOS arm64 / amd64"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$OUT/archify-view-darwin-arm64" .
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$OUT/archify-view-darwin-amd64" .

echo "==> checksums"
(cd "$OUT" && sha256sum archify-view-* > SHA256SUMS 2>/dev/null || shasum -a 256 archify-view-* > SHA256SUMS)
cat "$OUT/SHA256SUMS"
echo "version marker: $VERSION"
