#!/bin/sh
# 중앙 서버와 에이전트를 같은 장비에 설치한다. 기존 컨테이너, 서비스, 방화벽은 건드리지 않는다.
# 사용법: sudo ./install.sh --server home-1
#         필요한 파일: 이 스크립트 옆의 bin/msp-central, bin/msp-agent, dist/, 서비스 파일
set -eu

here=$(cd "$(dirname "$0")" && pwd)
server=""
while [ $# -gt 0 ]; do
    case "$1" in
    --server) server=${2:-}; shift 2 ;;
    *) echo "알 수 없는 옵션: $1" >&2; exit 2 ;;
    esac
done
[ -n "$server" ] || { echo "--server가 필요합니다" >&2; exit 2; }
[ "$(id -u)" -eq 0 ] || { echo "sudo로 실행하세요" >&2; exit 2; }
for path in bin/msp-central bin/msp-agent dist/index.html msp.slice msp-central.service msp-agent.service; do
    [ -e "$here/$path" ] || { echo "$path가 없습니다" >&2; exit 2; }
done

sh "$here/preflight.sh"

for name in msp-central msp-agent; do
    id "$name" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$name"
done

install -d -m 755 /opt/msp /etc/msp
install -m 755 "$here/bin/msp-central" "$here/bin/msp-agent" /opt/msp/
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

systemctl enable msp-central.service msp-agent.service
# 멈춰 있으면 시작하고, 업그레이드면 새 프로그램으로 다시 띄운다.
systemctl restart msp-central.service msp-agent.service

sleep 3
systemctl --no-pager --lines=0 status msp-central.service msp-agent.service || true
echo
echo "설치했습니다. 화면: ssh -L 8787:127.0.0.1:8787 <서버> 후 http://127.0.0.1:8787"
echo "부하 확인: sudo $here/loadcheck.sh    제거: sudo $here/uninstall.sh"
