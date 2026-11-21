#!/bin/sh
# MSP만 제거한다. 기본값은 설정과 수집 기록을 남긴다. --purge를 주면 그것까지 지운다.
set -eu

purge=0
[ "${1:-}" = "--purge" ] && purge=1
[ "$(id -u)" -eq 0 ] || { echo "sudo로 실행하세요" >&2; exit 2; }

systemctl disable --now msp-agent.service msp-central.service 2>/dev/null || true
rm -f /etc/systemd/system/msp-agent.service /etc/systemd/system/msp-central.service /etc/systemd/system/msp.slice
systemctl daemon-reload

# 화면 도우미는 사용자 서비스다. 돌고 있으면 멈춘 뒤 지운다.
screen_conf=/etc/systemd/user/msp-screen.service.d/user.conf
screen_user=$(sed -n 's/^ConditionUser=//p' "$screen_conf" 2>/dev/null || true)
if [ -n "$screen_user" ] && id "$screen_user" >/dev/null 2>&1; then
    screen_uid=$(id -u "$screen_user")
    if [ -S "/run/user/$screen_uid/bus" ]; then
        runuser -u "$screen_user" -- env XDG_RUNTIME_DIR="/run/user/$screen_uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$screen_uid/bus" \
            systemctl --user stop msp-screen.service 2>/dev/null || true
    fi
fi
systemctl --global disable msp-screen.service 2>/dev/null || true
rm -rf /etc/systemd/user/msp-screen.service /etc/systemd/user/msp-screen.service.d
rm -rf /opt/msp

if [ "$purge" -eq 1 ]; then
    rm -rf /etc/msp /var/lib/msp-central /var/lib/msp-agent
    for name in msp-agent msp-central; do
        id "$name" >/dev/null 2>&1 && userdel "$name"
    done
    echo "프로그램, 설정, 수집 기록, 사용자를 모두 지웠습니다."
else
    echo "프로그램과 서비스를 지웠습니다. 설정과 기록은 /etc/msp, /var/lib/msp-*에 남아 있습니다."
fi
