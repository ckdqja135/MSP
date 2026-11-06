#!/usr/bin/env python3
"""실제 Docker가 있는 서버에서 에이전트와 중앙 서버를 시험한다.

이미 열려 있는 SSH 공유 연결로 원격 명령을 실행하고, 터널로 API를 읽는다.
시험은 msp-test 프로젝트의 컨테이너만 만들고 바꾼다. 다른 컨테이너는 건드리지 않고 시작 전후를 비교한다.

사용법: real_docker.py --socket ~/.ssh/msp-test.sock --base http://127.0.0.1:18787/api/v1
"""
import argparse
import json
import shlex
import subprocess
import sys
import time
import urllib.parse
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--socket', required=True)
parser.add_argument('--base', required=True)
parser.add_argument('--server', default='test-server')
parser.add_argument('--screenshot', default='', help='{name}이 들어간 화면 저장 명령')
args = parser.parse_args()

PROFILES = '--profile extra --profile oom --profile crash --profile burst'
results = []


def check(name, ok, extra=''):
    results.append(bool(ok))
    print(('PASS' if ok else 'FAIL'), name, extra, flush=True)


def note(text):
    print(' ', text, flush=True)


def remote(command, ok_codes=(0,), stdin=None):
    # 실행하는 명령을 서버의 commands.log에 남겨 tail -f로 지켜볼 수 있게 한다.
    logged = f'printf "%s %s\\n" "$(date +%T)" {shlex.quote(command)} >> commands.log'
    done = subprocess.run(
        ['ssh', '-S', args.socket, '-o', 'BatchMode=yes', 'x', f'cd ~/msp-test || exit 1\n{logged}\n{command}'],
        input=stdin, capture_output=True, text=True, timeout=240,
    )
    if done.returncode not in ok_codes:
        raise RuntimeError(f'원격 명령 실패({done.returncode}): {command}\n{done.stderr[:500]}')
    return done.stdout.strip()


def api(path):
    with urllib.request.urlopen(args.base + path, timeout=15) as response:
        return json.load(response)


def wait(predicate, timeout, every=1.0):
    end = time.time() + timeout
    while time.time() < end:
        try:
            if predicate():
                return True
        except Exception:
            pass
        time.sleep(every)
    return False


def server():
    return api('/servers')[0]


def topology():
    return api(f'/servers/{args.server}/topology')


def service(name):
    for project in topology()['projects']:
        for item in project['services']:
            if item['key'] == f'msp-test/{name}':
                return item
    return None


def events(name=None, limit=500):
    query = {'limit': limit}
    if name:
        query['service'] = f'msp-test/{name}'
    return api(f'/servers/{args.server}/events?' + urllib.parse.urlencode(query))


def alerts():
    return api('/alerts')


def logs(**query):
    url = f'{args.base}/servers/{args.server}/logs?' + urllib.parse.urlencode(query)
    chunks = []
    with urllib.request.urlopen(url, timeout=30) as response:
        for raw in response:
            line = raw.decode()
            if line.startswith('data: '):
                chunk = json.loads(line[6:])
                chunks.append(chunk)
                if chunk.get('done'):
                    break
    return chunks


def lines_of(chunks):
    return [line for chunk in chunks for line in chunk.get('lines', [])]


def shot(name):
    if args.screenshot:
        subprocess.run(args.screenshot.format(name=name), shell=True, capture_output=True, timeout=60)


def start_agent():
    remote('GOMEMLIMIT=64MiB nohup nice -n 19 ./bin/msp-agent --config agent.json >> logs/agent.log 2>&1 < /dev/null & echo $! > agent.pid')


def start_central():
    remote('GOMEMLIMIT=128MiB nohup nice -n 19 ./bin/msp-central --db data/msp.db --web-dir dist --listen 127.0.0.1:18787 >> logs/central.log 2>&1 < /dev/null & echo $! > central.pid')


def compose(command):
    return remote(f'docker compose -f compose.yaml {command} 2>&1')


def canary_hits():
    stored = remote("cat data/msp.db* 2>/dev/null | grep -a -c msp-canary || true")
    spooled = remote("cat data/spool/* 2>/dev/null | grep -a -c msp-canary || true")
    logged = remote("cat logs/* | grep -a -c msp-canary || true")
    return int(stored or 0), int(spooled or 0), int(logged or 0)


image_existed = remote('docker image inspect busybox:1.37 >/dev/null 2>&1 && echo yes || echo no') == 'yes'
try:
    # 기존 컨테이너가 쓰지 않는 포트인지 먼저 확인한다.
    check('시험 포트가 비어 있음', not remote("ss -tln | grep ':18790 ' || true"))
    config = {
        'central_url': 'http://127.0.0.1:18787', 'server_id': args.server, 'token_file': 'token',
        'docker_socket': '/var/run/docker.sock', 'spool_dir': 'data/spool', 'spool_max_mb': 50, 'disk_path': '/',
        'probes': [
            {'service': 'msp-test/api', 'port': 80, 'path': '/health', 'timeout_ms': 2000},
            {'service': 'msp-test/web', 'port': 80, 'path': '/health', 'timeout_ms': 2000},
        ],
    }
    remote('cat > agent.json', stdin=json.dumps(config))
    remote('kill $(cat agent.pid)')
    time.sleep(1)
    start_agent()

    print('--- 추가', flush=True)
    started = time.time()
    compose('up -d')
    check('구성 변화가 자동으로 나타남', wait(lambda: service('api') and service('db') and service('web'), 20), f'{time.time() - started:.1f}초')
    relations = topology()['relations']
    mine = [item for item in relations if 'msp-test' in item['from'] or 'msp-test' in item['to']]
    declared = sorted((item['from'], item['to']) for item in mine if item['type'] == 'declared_depends_on')
    check('Compose 선언 의존을 실제 라벨에서 읽음', declared == [('msp-test/api', 'msp-test/db'), ('msp-test/web', 'msp-test/api')], str(declared))
    check('같은 네트워크로 서비스끼리 잇지 않음', all(item['to'].startswith('network:') for item in mine if item['type'] == 'member_of'))
    web = service('web')['containers'][0]
    check('루프백 바인딩을 구분', any(port.get('host_ip') == '127.0.0.1' and port.get('host_port') == 18790 for port in web['ports']))
    api_ports = service('api')['containers'][0].get('ports') or []
    check('공개하지 않은 포트를 공개로 표시하지 않음', any(port['container_port'] == 8080 and not port.get('host_port') for port in api_ports), str(api_ports))
    check('여러 네트워크 소속을 읽음', {item['network'] for item in web['networks']} == {'msp-test_front', 'msp-test_back'})

    print('--- 이벤트와 헬스체크', flush=True)
    check('실제 Docker 이벤트 기록', wait(lambda: {'container.create', 'container.start'} <= {e['type'] for e in events()}, 15))
    check('헬스체크 healthy 수신', wait(lambda: any(e['type'] == 'container.health' and e['result'] == 'healthy' for e in events('web')), 40))
    check('헬스체크 상태 반영', wait(lambda: service('web')['containers'][0]['health'] == 'healthy', 20))
    kinds = {e['type'] for e in events()}
    note(f'이벤트 종류: {sorted(kinds)}')
    check('exec 이벤트를 기록하지 않음', not any('exec' in kind for kind in kinds))
    check('Docker 이벤트의 소요와 크기는 null', all(e['duration_ms'] is None and e['bytes'] is None for e in events() if e['category'] == 'ops'))

    print('--- 응답 확인과 지표', flush=True)
    check('실제 컨테이너 IP로 응답 확인', wait(lambda: service('api')['response'] == 'ok' and service('web')['response'] == 'ok', 45))
    probe = service('api')['probes'][0]
    check('응답 시간 측정', probe['latency_ms'] is not None, f"{probe['latency_ms']}ms")
    check('컨테이너 지표 수신', wait(lambda: 'container.cpu.percent' in service('api')['containers'][0]['metrics'], 40))
    metrics = service('api')['containers'][0]['metrics']
    limit = metrics['container.mem.limit_bytes']['value']
    check('메모리 제한을 실제로 읽음', limit == 32 * 1024 * 1024, f'{limit / 1048576:.0f}MiB')
    check('응답 미설정 서비스는 응답 미확인', service('db')['response'] == 'unconfirmed')

    print('--- 비밀값', flush=True)
    check('환경 변수, 명령, 라벨의 비밀값이 저장되지 않음', canary_hits() == (0, 0, 0), str(canary_hits()))

    print('--- 재생성', flush=True)
    before = service('api')
    old_id = before['containers'][0]['id']
    compose('up -d --force-recreate --no-deps api')
    check('재생성 후 같은 서비스에 새 ID', wait(lambda: service('api')['containers'][0]['id'] != old_id and service('api')['containers'][0]['state'] == 'running', 30))
    kinds = [e['type'] for e in events('api')]
    check('삭제와 생성 기록이 같은 서비스에 연결', 'container.destroy' in kinds and kinds.count('container.create') >= 2)
    points = api(f"/servers/{args.server}/samples?" + urllib.parse.urlencode({'scope': 'msp-test/api#1', 'name': 'container.mem.bytes', 'points': 100}))
    check('재생성 전 지표가 같은 계열에 남음', len(points['series'][0]['points']) >= 1)
    check('재생성 후 응답 확인이 새 IP로 이어짐', wait(lambda: service('api')['response'] == 'ok', 45))

    print('--- 추가와 삭제', flush=True)
    web_id = service('web')['containers'][0]['id']
    compose('--profile extra up -d --no-deps extra')
    check('새 서비스 등장', wait(lambda: service('extra') is not None, 20))
    compose('--profile extra rm -sf extra')
    check('삭제된 서비스 사라짐', wait(lambda: service('extra') is None, 20))
    check('다른 서비스는 그대로', service('web')['containers'][0]['id'] == web_id)

    print('--- 실행 중 무응답', flush=True)
    remote("docker exec msp-test-api-1 sh -c 'kill -STOP $(pidof httpd)'")
    check('컨테이너는 실행 중, 응답만 실패', wait(lambda: service('api')['response'] == 'down', 130, 2), service('api')['response'])
    current = service('api')
    check('실행 상태와 응답 상태를 구분', current['runtime'] == 'running' and current['status'] == 'critical', str(current['reasons'][:1]))
    check('서버는 정상으로 유지', server()['reachability']['state'] == 'ok')
    check('다른 서비스는 정상', service('web')['status'] == 'ok')
    failed = [e for e in events('api') if e['category'] == 'probe' and e['type'] in ('probe.failed', 'probe.down')]
    check('응답 없음을 측정값으로 남기지 않음', failed and all(e['duration_ms'] is None and e['duration_limit_ms'] == 2000 for e in failed))
    check('서비스 알림 발생', wait(lambda: any(a['target'] == 'svc:msp-test/api' and a['rule'] == 'service_down' and a['state'] == 'firing' for a in alerts()), 25))
    shot('unresponsive')
    remote("docker exec msp-test-api-1 sh -c 'kill -CONT $(pidof httpd)'")
    check('응답 복구', wait(lambda: service('api')['response'] == 'ok', 130, 2))
    check('알림 복구', wait(lambda: any(a['rule'] == 'service_down' and a['state'] == 'resolved' for a in alerts()), 30))
    check('복구 기록', any(e['type'] == 'probe.recovered' for e in events('api')))

    print('--- 메모리 초과', flush=True)
    compose('--profile oom up -d --no-deps oom')
    check('메모리 초과 종료 감지', wait(lambda: service('oom') and service('oom')['containers'][0]['state'] == 'exited', 60), '')
    oom = service('oom')['containers'][0]
    note(f"exit={oom['exit_code']} oom_killed={oom.get('oom_killed')}")
    check('OOM 상태와 exit 137', oom.get('oom_killed') is True and oom['exit_code'] == 137)
    check('OOM 이벤트 수신', wait(lambda: any(e['type'] == 'container.oom' for e in events('oom')), 15))
    check('OOM 알림', wait(lambda: any(a['rule'] == 'oom' and 'msp-test/oom' in a['title'] for a in alerts()), 25))
    check('비정상 종료는 장애', service('oom')['status'] == 'critical')

    print('--- 재시작 반복', flush=True)
    compose('--profile crash up -d --no-deps crash')
    check('재시작 반복 알림', wait(lambda: any(a['rule'] == 'restart_loop' and 'msp-test/crash' in a['title'] for a in alerts()), 60, 2))
    check('상한에서 멈춤', wait(lambda: service('crash')['containers'][0]['state'] == 'exited', 60))
    note(f"재시작 {service('crash')['containers'][0]['restart_count']}회, 기록 {len(events('crash'))}건")
    check('재시작 횟수 수집', service('crash')['containers'][0]['restart_count'] == 4)

    print('--- 로그', flush=True)
    chunks = logs(service='msp-test/api', tail=50)
    lines = lines_of(chunks)
    note(f'로그: {[line["stream"] + ":" + line["text"][:44] for line in lines[:4]]}')
    body = json.dumps(chunks, ensure_ascii=False)
    check('실제 Docker 로그 형식을 읽음', len(lines) >= 3 and all(line['at'] and not line['at'].startswith('0001') for line in lines))
    check('비밀값이 가려져 전달됨', 'msp-canary' not in body and '가려짐' in body)
    check('키워드 검색', len(lines_of(logs(service='msp-test/api', q='sample failure'))) == 1)
    check('가려진 값은 검색 불가', len(lines_of(logs(service='msp-test/api', q='msp-canary'))) == 0)
    future = '2000-01-01T00:00:00Z'
    check('시간 구간 조회', len(lines_of(logs(service='msp-test/api', until=future))) == 0)
    check('구성에 없는 서비스 거부', '구성에 없는' in logs(service='other/none')[0].get('error', ''))

    print('--- 로그 급증', flush=True)
    load_before = remote('cut -d" " -f1 /proc/loadavg')
    compose('--profile burst up -d --no-deps burst')
    check('출력 종료', wait(lambda: service('burst') and service('burst')['containers'][0]['state'] == 'exited', 120, 2))
    chunks = logs(service='msp-test/burst', tail=5000)
    lines = lines_of(chunks)
    dropped = sum(chunk.get('dropped', 0) for chunk in chunks)
    check('가장 최근 줄이 남음', lines and lines[-1]['text'] == 'burst done', f'{len(lines)}줄, 누락 {dropped}')
    load_after = remote('cut -d" " -f1 /proc/loadavg')
    note(f'부하 {load_before} -> {load_after}')
    check('급증 중에도 서버 상태 유지', server()['reachability']['state'] == 'ok')

    print('--- 중앙 서버 중단과 복구', flush=True)
    remote('kill $(cat central.pid)')
    time.sleep(25)
    queued = int(remote('ls data/spool | grep -c batch || true') or 0)
    check('중앙 중단 중 에이전트가 버퍼링', queued >= 1, f'대기 {queued}건')
    start_central()
    check('복구 후 재전송', wait(lambda: server()['reachability']['state'] == 'ok' and int(remote('ls data/spool | grep -c batch || true') or 0) <= 1, 40))
    rows = events()
    unique = {(e['occurred_at'], e['type'], e['source'], e['target'], e['container_id']) for e in rows}
    check('복구 후 기록 중복 없음', len(unique) == len(rows), f'{len(rows)}건')

    print('--- 에이전트 중단', flush=True)
    remote('kill -9 $(cat agent.pid)')
    check('수신 끊김을 지연으로 표시', wait(lambda: server()['reachability']['state'] == 'delayed', 45))
    start_agent()
    check('재시작 후 공백 구간 기록', wait(lambda: any(gap['source'] == 'agent' for gap in api(f'/servers/{args.server}/gaps')), 25))
    check('재연결', wait(lambda: server()['reachability']['state'] == 'ok', 25))

    print('--- 마지막 확인', flush=True)
    check('비밀값이 끝까지 저장되지 않음', canary_hits() == (0, 0, 0), str(canary_hits()))
    logged = remote('cat token | xargs -I{} grep -c {} logs/agent.log logs/central.log || true')
    check('로그에 토큰 없음', all(line.endswith(':0') for line in logged.splitlines()), logged.replace('\n', ' '))
    shot('final')
    usage = remote('ps -o comm=,cputimes=,rss= -p $(cat agent.pid) -p $(cat central.pid)')
    note(f'자원(이름, 누적 CPU초, RSS KB): {usage.split()}')
finally:
    print('--- 정리', flush=True)
    compose(f'{PROFILES} down -v --remove-orphans')
    if not image_existed:
        remote('docker image rm busybox:1.37 >/dev/null 2>&1 || true')

left = remote("docker ps -a --filter label=com.docker.compose.project=msp-test -q | wc -l")
networks = remote("docker network ls --filter name=msp-test -q | wc -l")
check('시험 컨테이너와 네트워크 삭제', left == '0' and networks == '0', f'컨테이너 {left}, 네트워크 {networks}')
current = remote("docker ps -aq | xargs -r docker inspect --format '{{.Id}} {{.State.StartedAt}} {{.State.Status}} {{.RestartCount}}' | sort")
baseline = remote('cat baseline.txt')
check('기존 컨테이너 변화 없음 (ID, 시작 시각, 상태, 재시작 횟수)', current == baseline)
print(f'{sum(results)}/{len(results)} 통과', flush=True)
sys.exit(0 if all(results) else 1)
