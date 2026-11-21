#!/bin/sh
# 중앙 서버와 에이전트를 같은 장비에 설치한다. 기존 컨테이너, 서비스, 방화벽은 건드리지 않는다.
# 사용법: sudo ./install.sh --server home-1 [--screen-user 사용자] [--screen-control | --screen-view-only]
#         --screen-user를 주면 그 사용자의 데스크톱 화면을 화면보기로 볼 수 있다. 주지 않으면 화면은 읽지 않는다.
#         --screen-control을 주면 화면보기에서 마우스와 키보드로 서버를 조작할 수 있다. 기본은 보기 전용이다.
#         MSP 화면에 들어올 수 있는 사람은 누구나 조작할 수 있게 되므로, 화면을 루프백과 SSH 터널로만 열 때 켠다.
#         필요한 파일: 이 스크립트 옆의 bin/msp-central, bin/msp-agent, bin/msp-screen, dist/, 서비스 파일
set -eu

here=$(cd "$(dirname "$0")" && pwd)
server=""
screen_user=""
screen_control=""
while [ $# -gt 0 ]; do
    case "$1" in
    --server) server=${2:-}; shift 2 ;;
    --screen-user) screen_user=${2:-}; shift 2 ;;
    --screen-control) screen_control=true; shift ;;
    --screen-view-only) screen_control=false; shift ;;
    *) echo "알 수 없는 옵션: $1" >&2; exit 2 ;;
    esac
done
[ -n "$server" ] || { echo "--server가 필요합니다" >&2; exit 2; }
[ "$(id -u)" -eq 0 ] || { echo "sudo로 실행하세요" >&2; exit 2; }
for path in bin/msp-central bin/msp-agent bin/msp-screen dist/index.html msp.slice msp-central.service msp-agent.service msp-screen.service; do
    [ -e "$here/$path" ] || { echo "$path가 없습니다" >&2; exit 2; }
done
# 업그레이드할 때는 앞서 지정한 화면 사용자를 그대로 쓴다.
screen_conf=/etc/systemd/user/msp-screen.service.d/user.conf
if [ -z "$screen_user" ] && [ -e "$screen_conf" ]; then
    screen_user=$(sed -n 's/^ConditionUser=//p' "$screen_conf")
fi
if [ -n "$screen_user" ]; then
    id "$screen_user" >/dev/null 2>&1 || { echo "사용자 $screen_user가 없습니다" >&2; exit 2; }
fi
# 조작 허용 여부도 따로 말하지 않으면 앞서 정한 대로 둔다. 처음에는 보기 전용이다.
if [ -z "$screen_control" ]; then
    screen_control=false
    grep -q '"allow_control": *true' /etc/msp/screen.json 2>/dev/null && screen_control=true
fi

sh "$here/preflight.sh"

for name in msp-central msp-agent; do
    id "$name" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$name"
done

install -d -m 755 /opt/msp /etc/msp
install -m 755 "$here/bin/msp-central" "$here/bin/msp-agent" "$here/bin/msp-screen" /opt/msp/
rm -rf /opt/msp/dist
cp -R "$here/dist" /opt/msp/dist
chmod -R a+rX /opt/msp/dist

# 설정이 이미 있으면 그대로 둔다. 웹훅과 heartbeat 주소에 비밀값이 들어 있을 수 있다.
if [ ! -e /etc/msp/central.json ]; then
    (umask 027; printf '{\n  "same_host_servers": ["%s"]\n}\n' "$server" > /etc/msp/central.json)
fi
chown root:msp-central /etc/msp/central.json
chmod 640 /etc/msp/central.json
if [ ! -e /etc/msp/agent.json ]; then
    # nginx 접근 로그가 있으면 실제 API 요청을 읽는다. 로그를 읽기만 하고 nginx 설정은 바꾸지 않는다.
    access_logs='[]'
    if [ -e /var/log/nginx/access.log ]; then
        access_logs='[{ "service": "nginx", "path": "/var/log/nginx/access.log" }]'
    fi
    (umask 027; cat > /etc/msp/agent.json <<JSON
{
  "central_url": "http://127.0.0.1:8787",
  "server_id": "$server",
  "token_file": "/etc/msp/agent-token",
  "docker_socket": "/var/run/docker.sock",
  "spool_dir": "/var/lib/msp-agent/spool",
  "spool_max_mb": 50,
  "disk_path": "/",
  "access_logs": $access_logs
}
JSON
)
fi
chown root:msp-agent /etc/msp/agent.json
chmod 640 /etc/msp/agent.json

install -m 644 "$here/msp.slice" "$here/msp-central.service" "$here/msp-agent.service" /etc/systemd/system/
systemctl daemon-reload

# 토큰은 파일로만 남긴다. 화면에 출력하지 않는다.
install -d -o msp-central -g msp-central -m 700 /var/lib/msp-central
if [ ! -s /etc/msp/agent-token ]; then
    (umask 027; runuser -u msp-central -- /opt/msp/msp-central enroll --db /var/lib/msp-central/msp.db --server "$server" > /etc/msp/agent-token)
fi
chown root:msp-agent /etc/msp/agent-token
chmod 640 /etc/msp/agent-token

# 화면 도우미는 데스크톱에 로그인한 사용자의 세션 안에서 돈다. 사용자를 지정했을 때만 켠다.
# 토큰은 그 사용자만 읽을 수 있고, 에이전트 토큰과 달라 화면 전송에만 쓸 수 있다.
if [ -n "$screen_user" ]; then
    install -d -m 755 /etc/systemd/user/msp-screen.service.d
    install -m 644 "$here/msp-screen.service" /etc/systemd/user/
    printf '[Unit]\nConditionUser=%s\n' "$screen_user" > "$screen_conf"
    chmod 644 "$screen_conf"
    # 비밀값이 없는 파일이라 매번 새로 쓴다. 토큰은 따로 둔다.
    cat > /etc/msp/screen.json <<JSON
{
  "central_url": "http://127.0.0.1:8787",
  "server_id": "$server",
  "token_file": "/etc/msp/screen-token",
  "allow_control": $screen_control
}
JSON
    chmod 644 /etc/msp/screen.json
    if [ ! -s /etc/msp/screen-token ]; then
        (umask 077; runuser -u msp-central -- /opt/msp/msp-central enroll --db /var/lib/msp-central/msp.db --server "$server" --screen > /etc/msp/screen-token)
    fi
    chown "$screen_user" /etc/msp/screen-token
    chmod 400 /etc/msp/screen-token
    systemctl --global enable msp-screen.service
fi

systemctl enable msp-central.service msp-agent.service
# 멈춰 있으면 시작하고, 업그레이드면 새 프로그램으로 다시 띄운다.
systemctl restart msp-central.service msp-agent.service

sleep 3
systemctl --no-pager --lines=0 status msp-central.service msp-agent.service || true
echo
if [ -n "$screen_user" ]; then
    # 그 사용자가 데스크톱에 로그인해 있으면 바로 띄운다. 아니면 다음 로그인 때 시작된다.
    screen_uid=$(id -u "$screen_user")
    as_screen_user() {
        runuser -u "$screen_user" -- env XDG_RUNTIME_DIR="/run/user/$screen_uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$screen_uid/bus" "$@"
    }
    if [ -S "/run/user/$screen_uid/bus" ] && as_screen_user systemctl --user daemon-reload &&
        as_screen_user systemctl --user is-active --quiet graphical-session.target; then
        if as_screen_user systemctl --user restart msp-screen.service; then
            echo "화면 도우미를 시작했습니다 (사용자 $screen_user). 확인: /opt/msp/msp-screen --check 를 그 사용자의 데스크톱 터미널에서 실행"
            if [ "$screen_control" = true ]; then
                echo "원격 조작을 허용했습니다. 화면보기에서 조작을 켠 동안 마우스와 키보드 입력이 이 서버로 들어옵니다. 끄려면 --screen-view-only로 다시 설치하세요."
            else
                echo "화면은 보기 전용입니다. 조작하려면 --screen-control을 붙여 다시 설치하세요."
            fi
        else
            echo "화면 도우미를 시작하지 못했습니다. 확인: systemctl --user status msp-screen.service (사용자 $screen_user)"
        fi
    else
        echo "사용자 $screen_user가 데스크톱에 로그인해 있지 않습니다. 로그인하면 화면 도우미가 시작됩니다."
    fi
else
    echo "화면보기의 데스크톱 화면은 꺼져 있습니다. 켜려면 --screen-user <데스크톱 사용자>를 붙여 다시 실행하세요."
fi
echo "설치했습니다. 화면: ssh -L 8787:127.0.0.1:8787 <서버> 후 http://127.0.0.1:8787"
echo "부하 확인: sudo $here/loadcheck.sh    제거: sudo $here/uninstall.sh"
