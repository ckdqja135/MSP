#!/bin/sh
# 설치나 설정 없이 이 컴퓨터를 바로 본다. Docker 컨테이너와 열린 포트가 나온다.
# 필요한 것: Go, Node.js. 사용법: ./local.sh  (끝내려면 Ctrl+C)
set -eu
root=$(cd "$(dirname "$0")" && pwd)
cd "$root/web"
[ -d node_modules ] || npm install
npm run build
cd "$root"
exec go run ./cmd/central --local "$@"
