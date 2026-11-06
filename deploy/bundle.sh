#!/bin/sh
# 서버에 올릴 설치 묶음을 만든다. 비밀값은 들어가지 않는다. 사용법: deploy/bundle.sh [amd64|arm64]
set -eu

arch=${1:-amd64}
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
out="$work/msp-deploy"
mkdir -p "$out/bin"

(cd "$root/web" && npm run build >/dev/null)
for name in central agent; do
    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$out/bin/msp-$name" "./cmd/$name")
done
cp -R "$root/web/dist" "$out/dist"
cp "$root/deploy/msp.slice" "$root/deploy/msp-central.service" "$root/deploy/msp-agent.service" \
    "$root/deploy/preflight.sh" "$root/deploy/install.sh" "$root/deploy/uninstall.sh" "$root/deploy/loadcheck.sh" "$out/"
COPYFILE_DISABLE=1 tar -C "$work" --no-xattrs -czf "$root/msp-deploy-$arch.tar.gz" msp-deploy
echo "$root/msp-deploy-$arch.tar.gz"
