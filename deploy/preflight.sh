#!/bin/sh
# 설치 전 점검이다. 읽기만 하고 아무것도 바꾸지 않는다. sudo가 필요 없다.
here=$(cd "$(dirname "$0")" && pwd)
problems=0
ok() { echo "확인  $1"; }
bad() { echo "문제  $1"; problems=$((problems + 1)); }
note() { echo "참고  $1"; }

command -v systemctl >/dev/null 2>&1 && ok "systemd $(systemctl --version | head -1 | cut -d' ' -f2)" || bad "systemd가 없습니다"

if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = "cgroup2fs" ]; then
    ok "cgroup v2"
    root_controllers=$(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)
    for controller in cpu memory pids; do
        case " $root_controllers " in
        *" $controller "*) ok "최상위 $controller 컨트롤러가 이미 켜져 있음" ;;
        *) note "최상위 $controller 컨트롤러가 꺼져 있습니다. 상한을 걸면 systemd가 켭니다" ;;
        esac
    done
else
    bad "cgroup v2가 아닙니다. 자원 상한이 다르게 동작할 수 있습니다"
fi

[ -S /var/run/docker.sock ] && ok "Docker 소켓 있음" || bad "Docker 소켓이 없습니다"
getent group docker >/dev/null 2>&1 && ok "docker 그룹 있음" || bad "docker 그룹이 없습니다"

if systemctl is-active --quiet msp-central.service 2>/dev/null; then
    note "msp-central이 이미 8787 포트에서 동작 중입니다. 업그레이드로 진행합니다"
elif ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ':8787$'; then
    bad "8787 포트를 이미 쓰고 있습니다"
else
    ok "8787 포트 비어 있음"
fi

for name in msp-central msp-agent; do
    if systemctl cat "$name.service" >/dev/null 2>&1; then
        note "$name 서비스가 이미 있습니다. 설치하면 프로그램과 서비스 파일을 새 것으로 바꿉니다"
    fi
    if id "$name" >/dev/null 2>&1; then
        note "$name 사용자가 이미 있습니다"
    fi
done
[ -e /opt/msp ] && note "/opt/msp가 이미 있습니다"

if [ -e /var/log/nginx/access.log ]; then
    ok "nginx 접근 로그 있음 (소유 그룹 $(stat -c %G /var/log/nginx/access.log 2>/dev/null))"
else
    note "nginx 접근 로그가 없어 API 요청은 표시되지 않습니다"
fi

available=$(df -Pm /var/lib 2>/dev/null | awk 'NR==2 {print $4}')
if [ -n "$available" ] && [ "$available" -ge 2048 ]; then
    ok "/var/lib 여유 공간 ${available}MB"
else
    bad "/var/lib 여유 공간이 2GB보다 적습니다 (${available:-알 수 없음}MB)"
fi

if command -v systemd-analyze >/dev/null 2>&1; then
    for unit in msp.slice msp-central.service msp-agent.service; do
        # /opt/msp가 아직 없어 실행 파일 경고는 설치 전이면 정상이다.
        errors=$(systemd-analyze verify "$here/$unit" 2>&1 | grep -v 'is not executable\|Command .* is not\|Unknown .*msp' | grep "$unit" || true)
        if [ -n "$errors" ]; then
            bad "$unit 검사: $errors"
        else
            ok "$unit 문법"
        fi
    done
fi

echo
if [ "$problems" -eq 0 ]; then
    echo "설치를 막는 문제가 없습니다."
else
    echo "문제 ${problems}개를 먼저 해결해야 합니다."
    exit 1
fi
