import { describe, expect, it } from 'vitest'
import type { Container, HostService, Probe, Project, Relation, RequestRow, Service, Topology } from './api'
import { appendLiveSteps, buildTimeline, entryDevice, LIVE_MAX_STEPS, liveRequestScript, frameAt, hopProgress, nextArrival, observedScript, overviewFrame, previousStepStart, requestScope, requestScript, scopedEmptyText, simulationScript, upstreamDevice } from './flow'

const container = (overrides: Partial<Container> = {}): Container =>
  ({ id: 'c', name: 'c', image: 'app:1', state: 'running', exit_code: 0, health: 'none', restart_count: 0, created_at: '', started_at: '', replica: 1, project: '', service: '', metrics: {}, ...overrides }) as Container

const service = (key: string, overrides: Partial<Service> = {}, containers: Container[] = [container()]): Service => ({
  key, name: key.split('/')[1], project: key.split('/')[0], status: 'ok', runtime: 'running', response: 'unconfirmed', reasons: [], containers, probes: [], ...overrides,
}) as Service

const published = (port: number, host_ip = '0.0.0.0') => [container({ ports: [{ host_ip, host_port: port, container_port: port, proto: 'tcp' }] })]
const depends = (from: string, to: string): Relation => ({ type: 'declared_depends_on', from, to, evidence: 'compose-label' })
const topology = (services: Service[], relations: Relation[] = [], host_services: HostService[] = []): Topology =>
  ({ projects: services.length ? ([{ key: 'blog', status: 'ok', services }] as Project[]) : [], networks: [], relations, host_services, historical: false }) as unknown as Topology
const nginx: HostService = { name: 'nginx', evidence: 'config', listening: true, ports: [{ ip: '0.0.0.0', port: 443 }, { ip: '0.0.0.0', port: 80 }] } as HostService
const probe = (overrides: Partial<Probe> = {}): Probe => ({ vantage: 'internal', target: 'GET /health', state: 'ok', failures: 0, last_checked: '2026-11-06T03:00:00Z', latency_ms: 12, ...overrides })

describe('시뮬레이션 대본', () => {
  const chain = topology(
    [service('blog/db'), service('blog/api'), service('blog/web', {}, published(443))],
    [depends('blog/web', 'blog/api'), depends('blog/api', 'blog/db')],
  )

  it('공개 포트로 들어와 선언 의존을 따라갔다가 되돌아 나온다', () => {
    const script = simulationScript(chain)!
    const [connect, request, response] = script.steps
    expect(connect.packets.map((packet) => packet.title)).toEqual(['SYN', 'SYN, ACK', 'ACK'])
    expect(request.packets[0].path).toEqual(['internet', 'host', 'svc:blog/web'])
    expect(request.packets.map((packet) => packet.path[packet.path.length - 1])).toEqual(['svc:blog/web', 'svc:blog/api', 'svc:blog/db'])
    expect(response.packets.map((packet) => packet.path[packet.path.length - 1])).toEqual(['svc:blog/api', 'svc:blog/web', 'internet'])
  })

  it('측정하지 않은 시간과 소요를 만들어 내지 않는다', () => {
    const script = simulationScript(chain)!
    for (const packet of script.steps.flatMap((step) => step.packets)) {
      expect(packet.at).toBeNull()
      expect(packet.durationMS).toBeNull()
      expect(packet.result).toBe('예시')
    }
    expect(script.note).toContain('실제 통신이 아닙니다')
  })

  it('같은 네트워크라는 이유로 서비스를 잇지 않는다', () => {
    const script = simulationScript(topology([service('blog/web', {}, published(80)), service('blog/db')]))!
    const devices = script.steps.flatMap((step) => step.packets.flatMap((packet) => packet.path))
    expect(devices).not.toContain('svc:blog/db')
  })

  it('고른 서비스를 지나는 경로를 먼저 고른다', () => {
    const data = topology(
      [service('blog/web', {}, published(80)), service('blog/api'), service('blog/db'), service('blog/admin', {}, published(9000))],
      [depends('blog/web', 'blog/api'), depends('blog/api', 'blog/db'), depends('blog/admin', 'blog/db')],
    )
    expect(simulationScript(data, 'svc:blog/admin')!.steps[0].packets[0].path).toContain('svc:blog/admin')
  })

  it('멈춘 서비스로 이어 그리지 않는다', () => {
    const data = topology([service('blog/web', {}, published(80)), service('blog/db', { runtime: 'stopped' })], [depends('blog/web', 'blog/db')])
    const devices = simulationScript(data)!.steps.flatMap((step) => step.packets.flatMap((packet) => packet.path))
    expect(devices).not.toContain('svc:blog/db')
  })

  it('루프백만 열린 서비스는 입구가 아니다', () => {
    expect(simulationScript(topology([service('blog/web', {}, published(8080, '127.0.0.1'))]))).toBeUndefined()
  })

  it('호스트 nginx는 입구로 쓰되 뒤쪽을 지어내지 않는다', () => {
    const script = simulationScript(topology([service('blog/db')], [], [nginx]))!
    const devices = script.steps.flatMap((step) => step.packets.flatMap((packet) => packet.path))
    expect(script.steps[1].packets[0].path).toEqual(['internet', 'host', 'hostsvc:nginx'])
    expect(devices).not.toContain('svc:blog/db')
    expect(script.steps[1].description).toContain('설정을 읽지 않아')
  })

  it('입구 뒤를 모르면 고른 서비스로 간다고 가정하고 그렇게 밝힌다', () => {
    const data = topology([service('blog/web'), service('blog/db')], [depends('blog/web', 'blog/db')], [nginx])
    const script = simulationScript(data, 'svc:blog/web')!
    const paths = script.steps[1].packets.map((packet) => packet.path)
    expect(paths[1]).toEqual(['hostsvc:nginx', 'svc:blog/web'])
    expect(paths[2]).toEqual(['svc:blog/web', 'svc:blog/db'])
    expect(script.steps[1].description).toContain('가정합니다')
    expect(script.steps[2].packets.map((packet) => packet.path[packet.path.length - 1])).toEqual(['svc:blog/web', 'hostsvc:nginx', 'internet'])
  })

  it('고른 서비스가 멈춰 있으면 이어 그리지 않는다', () => {
    const data = topology([service('blog/web', { runtime: 'stopped' })], [], [nginx])
    const devices = simulationScript(data, 'svc:blog/web')!.steps.flatMap((step) => step.packets.flatMap((packet) => packet.path))
    expect(devices).not.toContain('svc:blog/web')
  })

  it('지금 장애인 서비스를 예시와 함께 알린다', () => {
    const data = topology([service('blog/web', {}, published(80)), service('blog/api', { status: 'critical' })], [depends('blog/web', 'blog/api')])
    const fact = simulationScript(data)!.facts.find(([label]) => label === '현재 장애')
    expect(fact?.[1]).toContain('api')
  })

  it('들어올 길이 없으면 대본을 만들지 않는다', () => {
    expect(simulationScript(topology([service('blog/db')]))).toBeUndefined()
  })
})

describe('응답 확인 기록', () => {
  it('성공은 요청과 응답을 측정값과 함께 보여준다', () => {
    const script = observedScript(topology([service('blog/api', { probes: [probe()] })]))!
    const [request, response] = script.steps[0].packets
    expect(request.path).toEqual(['host', 'svc:blog/api'])
    expect(request.durationMS).toBeNull()
    expect(response.path).toEqual(['svc:blog/api', 'host'])
    expect(response.durationMS).toBe(12)
    expect(response.at).toBe('2026-11-06T03:00:00Z')
  })

  it('실패는 돌아오는 패킷을 그리지 않는다', () => {
    const script = observedScript(topology([service('blog/api', { probes: [probe({ state: 'down', failures: 3, latency_ms: null, last_failure: '제한 시간 초과' })] })]))!
    expect(script.steps[0].packets).toHaveLength(1)
    expect(script.steps[0].packets[0].kind).toBe('fail')
    expect(script.steps[0].packets[0].detail).toBe('제한 시간 초과')
    expect(script.steps[0].packets[0].durationMS).toBeNull()
  })

  it('외부 응답 확인은 외부에서 출발한다', () => {
    const script = observedScript(topology([service('blog/api', { probes: [probe({ vantage: 'external', target: 'GET https://blog.example.test/health' })] })]))!
    expect(script.steps[0].packets[0].path[0]).toBe('internet')
    expect(script.steps[0].packets[0].title).toBe('GET /health')
  })

  it('데모에서는 실제 기록이라고 말하지 않는다', () => {
    const data = topology([service('blog/api', { probes: [probe()] })])
    expect(observedScript(data, undefined, true)!.note).toContain('가짜')
    expect(observedScript(data)!.note).toContain('실제로 보낸')
  })

  it('응답 확인이 없으면 대본을 만들지 않는다', () => {
    expect(observedScript(topology([service('blog/api')]))).toBeUndefined()
  })

  it('고른 서비스만 재생한다', () => {
    const data = topology([service('blog/api', { probes: [probe()] }), service('blog/web', { probes: [probe()] })])
    expect(observedScript(data, 'svc:blog/web')!.steps.map((step) => step.id)).toEqual(['blog/web'])
  })
})

describe('시간표', () => {
  const script = simulationScript(topology([service('blog/web', {}, published(80)), service('blog/db')], [depends('blog/web', 'blog/db')]))!
  const timeline = buildTimeline(script)

  it('처음에는 아무 패킷도 도착하지 않았고 출발 장비만 보인다', () => {
    const frame = frameAt(timeline, 0)
    expect(frame.phase).toBe('intro')
    expect(frame.delivered).toBe(0)
    expect(frame.revealed).toEqual(['internet'])
    expect(frame.camera).toEqual(['internet'])
  })

  it('패킷이 지나가면서 장비가 하나씩 드러난다', () => {
    const hop = timeline.segments.find((segment) => segment.kind === 'hop' && segment.hop === 1)!
    const frame = frameAt(timeline, hop.start + 1)
    expect(frame.revealed).toEqual(['internet', 'host', 'svc:blog/web'])
    expect(frame.delivered).toBe(0)
  })

  it('도착하는 순간 표에 한 줄이 늘어난다', () => {
    expect(frameAt(timeline, timeline.arrivals[0] - 1).delivered).toBe(0)
    expect(frameAt(timeline, timeline.arrivals[0]).delivered).toBe(1)
    expect(frameAt(timeline, timeline.arrivals[2]).delivered).toBe(3)
  })

  it('끝나면 모든 패킷이 도착한 상태다', () => {
    const frame = frameAt(timeline, timeline.total)
    expect(frame.phase).toBe('done')
    expect(frame.delivered).toBe(timeline.packets.length)
  })

  it('개요는 전체 패킷을 보여준다', () => {
    expect(overviewFrame(timeline).delivered).toBe(timeline.packets.length)
    expect(overviewFrame(timeline).camera).toEqual([])
  })

  it('구간 안에서 패킷 위치가 0에서 1로 간다', () => {
    const hop = timeline.segments.find((segment) => segment.kind === 'hop')!
    expect(hopProgress(timeline, hop.start)).toBe(0)
    expect(hopProgress(timeline, hop.start + hop.duration / 2)).toBeCloseTo(0.5)
    expect(hopProgress(timeline, timeline.arrivals[0])).toBe(1)
  })

  it('한 패킷씩 볼 때 다음 도착 시각으로 간다', () => {
    expect(nextArrival(timeline, 0)).toBe(timeline.arrivals[0])
    expect(nextArrival(timeline, timeline.arrivals[0])).toBe(timeline.arrivals[1])
    expect(nextArrival(timeline, timeline.total)).toBe(timeline.total)
  })

  it('이전 단계로 돌아간다', () => {
    const second = timeline.stepStarts[1]
    expect(previousStepStart(timeline, second + 1000)).toBe(second)
    expect(previousStepStart(timeline, second + 10)).toBe(0)
    expect(previousStepStart(timeline, 10)).toBe(0)
  })
})

describe('API 요청', () => {
  const request = (overrides: Partial<RequestRow> = {}): RequestRow => ({
    id: 1, at: '2026-11-06T03:00:00Z', service: 'nginx', method: 'GET', path: '/api/posts', proto: 'HTTP/1.1', status: 200, bytes: 1500, duration_ms: 52, upstream: '172.20.0.3:8080', upstream_ms: 48, ...overrides,
  })
  const api = service('blog/api', {}, [container({ networks: [{ network: 'blog_default', ip: '172.20.0.3' }] })])
  const data = topology([api], [], [nginx])

  it('외부에서 프록시와 서비스를 거쳐 되돌아 나온다', () => {
    const packets = requestScript([request()], data)!.steps[0].packets
    expect(packets.map((packet) => packet.path)).toEqual([
      ['internet', 'host', 'hostsvc:nginx'],
      ['hostsvc:nginx', 'svc:blog/api'],
      ['svc:blog/api', 'hostsvc:nginx'],
      ['hostsvc:nginx', 'host', 'internet'],
    ])
    expect(packets[2].durationMS).toBe(48)
    expect(packets[3].durationMS).toBe(52)
    expect(packets[3].bytes).toBe(1500)
    expect(packets[3].result).toBe('200')
    expect(packets[0].at).toBe('2026-11-06T03:00:00Z')
  })

  it('로그에 없는 값은 비워 두고 뒤쪽을 지어내지 않는다', () => {
    const script = requestScript([request({ upstream: '', upstream_ms: null, duration_ms: null, bytes: null })], data)!
    const packets = script.steps[0].packets
    expect(packets).toHaveLength(2)
    expect(packets[1].durationMS).toBeNull()
    expect(packets[1].bytes).toBeNull()
    expect(script.steps[0].description).toContain('적혀 있지 않아')
    expect(packets.flatMap((packet) => packet.path)).not.toContain('svc:blog/api')
  })

  it('구성에 없는 전달 주소는 잇지 않고 그렇게 밝힌다', () => {
    const script = requestScript([request({ upstream: '10.9.9.9:80' })], data)!
    expect(script.steps[0].packets.flatMap((packet) => packet.path)).not.toContain('svc:blog/api')
    expect(script.steps[0].description).toContain('10.9.9.9:80')
  })

  it('5xx는 실패로 표시하고 전달 대상의 응답이 없으면 그리지 않는다', () => {
    const packets = requestScript([request({ status: 504, upstream_ms: null })], data)!.steps[0].packets
    expect(packets.map((packet) => packet.kind)).toEqual(['request', 'request', 'fail'])
    expect(packets[2].title).toBe('504')
  })

  it('4xx는 서버 장애로 표시하지 않는다', () => {
    const packets = requestScript([request({ status: 404, upstream: '', upstream_ms: null })], data)!.steps[0].packets
    expect(packets[packets.length - 1].kind).toBe('response')
  })

  it('최근 요청을 시간순으로 재생하고 개수를 제한한다', () => {
    const rows = Array.from({ length: 12 }, (_, index) => request({ id: index + 1, at: new Date(Date.UTC(2026, 10, 6, 3, 0, 12 - index)).toISOString() }))
    const script = requestScript(rows, data)!
    expect(script.steps).toHaveLength(8)
    expect(script.steps.map((step) => Number(step.id))).toEqual([8, 7, 6, 5, 4, 3, 2, 1])
    expect(script.facts[0][1]).toContain('12건')
  })

  it('요청이 없으면 대본을 만들지 않는다', () => {
    expect(requestScript([], data)).toBeUndefined()
  })

  it('데모는 가짜라고 밝히고 실제는 한계를 밝힌다', () => {
    expect(requestScript([request()], data, true)!.note).toContain('가짜')
    const note = requestScript([request()], data)!.note
    expect(note).toContain('실제 요청')
    expect(note).toContain('서비스끼리')
  })

  it('프록시를 구성에서 찾는다', () => {
    expect(entryDevice('nginx', data)).toBe('hostsvc:nginx')
    expect(entryDevice('blog/api', data)).toBe('svc:blog/api')
    expect(entryDevice('없음', data)).toBe('host')
    expect(requestScript([request({ service: '없음', upstream: '' })], data)!.steps[0].packets[0].path).toEqual(['internet', 'host'])
  })

  it('전달 주소를 컨테이너 IP와 루프백 공개 포트에서 찾는다', () => {
    const published = service('blog/web', {}, [container({ ports: [{ host_ip: '127.0.0.1', host_port: 18080, container_port: 80, proto: 'tcp' }] })])
    const both = topology([api, published])
    expect(upstreamDevice('172.20.0.3:8080', both)?.id).toBe('svc:blog/api')
    expect(upstreamDevice('127.0.0.1:18080', both)?.id).toBe('svc:blog/web')
    expect(upstreamDevice('127.0.0.1:9999', both)).toBeUndefined()
    expect(upstreamDevice('', both)).toBeUndefined()
  })

  it('IPv6 루프백과 모든 주소에 공개한 포트를 찾고 다른 주소에만 묶인 포트는 잇지 않는다', () => {
    const open = service('blog/web', {}, [container({ ports: [{ host_ip: '0.0.0.0', host_port: 8080, container_port: 80, proto: 'tcp' }, { host_ip: '::', host_port: 8080, container_port: 80, proto: 'tcp' }] })])
    const lan = service('blog/admin', {}, [container({ ports: [{ host_ip: '192.168.0.10', host_port: 9090, container_port: 80, proto: 'tcp' }] })])
    const data = topology([open, lan])
    expect(upstreamDevice('[::1]:8080', data)?.id).toBe('svc:blog/web')
    expect(upstreamDevice('127.0.0.1:8080', data)?.id).toBe('svc:blog/web')
    expect(upstreamDevice('127.0.0.1:9090', data)).toBeUndefined()
  })

  it('루프백 포트를 연 호스트 서비스도 전달 대상으로 찾는다', () => {
    const fpm: HostService = { name: 'php-fpm', ports: [{ ip: '127.0.0.1', port: 9000 }], evidence: 'name-match', listening: true }
    const data = topology([], [], [nginx, fpm])
    expect(upstreamDevice('127.0.0.1:9000', data)).toEqual({ id: 'hostsvc:php-fpm', name: 'php-fpm' })
    expect(upstreamDevice('10.0.0.5:9000', data)).toBeUndefined()
  })

  it('전달 대상을 몇 건 찾았는지 밝히고 기록이 없으면 로그 형식을 안내한다', () => {
    const rows = [request({ id: 1 }), request({ id: 2, upstream: '10.9.9.9:80' }), request({ id: 3, upstream: '', upstream_ms: null })]
    const facts = Object.fromEntries(requestScript(rows, data)!.facts)
    expect(facts['전달 대상']).toBe('1건 확인 · 1건 구성에서 못 찾음 · 1건 기록 없음')
    const none = requestScript([request({ upstream: '', upstream_ms: null })], data)!
    expect(Object.fromEntries(none.facts)['전달 대상']).toBe('로그에 기록 없음')
    expect(none.note).toContain('$upstream_addr')
    expect(requestScript(rows, data)!.note).not.toContain('$upstream_addr')
  })

  it('프록시가 자기 자신으로 보낸 요청은 선을 잇지 않는다', () => {
    const step = requestScript([request({ upstream: '127.0.0.1:80' })], topology([], [], [nginx]))!.steps[0]
    expect(step.packets).toHaveLength(2)
    expect(step.description).toContain('자기 자신')
  })
})

describe('고른 대상의 요청', () => {
  const request = (id: number, upstream: string): RequestRow => ({
    id, at: new Date(Date.UTC(2026, 10, 6, 3, 0, id)).toISOString(), service: 'nginx', method: 'GET', path: `/p/${id}`, proto: 'HTTP/1.1', status: 200, bytes: 10, duration_ms: 5, upstream, upstream_ms: 4,
  })
  const api = service('blog/api', {}, [container({ networks: [{ network: 'blog_default', ip: '172.20.0.3' }] })])
  const dev = service('blog/dev', {}, [container({ ports: [{ host_ip: '0.0.0.0', host_port: 3005, container_port: 3005, proto: 'tcp' }] })])
  const data = topology([api, dev], [], [nginx])
  const rows = [request(1, '172.20.0.3:8080'), request(2, '127.0.0.1:3005'), request(3, '172.20.0.3:8080'), request(4, '')]

  it('고른 것이 없거나 서버 전체면 좁히지 않는다', () => {
    expect(requestScope(undefined, data)).toBeUndefined()
    expect(requestScope('host', data)).toBeUndefined()
  })

  it('컨테이너를 고르면 그 컨테이너로 전달된 요청만 재생한다', () => {
    const script = requestScript(rows, data, false, requestScope('svc:blog/api', data))!
    expect(script.steps.map((step) => step.id)).toEqual(['1', '3'])
    expect(script.title).toContain('blog/api')
    expect(Object.fromEntries(script.facts)['대상']).toBe('blog/api · 조회한 4건 중 2건')
    expect(script.note).toContain('blog/api에 닿은 요청만')
  })

  it('프록시를 고르면 프록시가 받은 요청을 모두 재생한다', () => {
    expect(requestScript(rows, data, false, requestScope('hostsvc:nginx', data))!.steps).toHaveLength(4)
  })

  it('프로젝트를 고르면 그 안의 서비스에 닿은 요청을 재생한다', () => {
    expect(requestScript(rows, data, false, requestScope('project:blog', data))!.steps.map((step) => step.id)).toEqual(['1', '2', '3'])
  })

  it('대상에 닿은 요청이 없으면 전체로 대신 채우지 않고 직접 접속은 기록되지 않는다고 밝힌다', () => {
    const scope = requestScope('svc:blog/dev', data)!
    expect(requestScript([request(1, '172.20.0.3:8080')], data, false, scope)).toBeUndefined()
    expect(scope.published).toBe(true)
    const text = scopedEmptyText(scope, 1)
    expect(text).toContain('조회한 요청 1건 중')
    expect(text).toContain('직접 들어온 접속')
    expect(scopedEmptyText(requestScope('svc:blog/api', data)!, 0)).not.toContain('직접 들어온 접속')
  })

  it('실시간에서는 대상에 닿은 요청만 덧붙이고 건너뛴 요청도 다시 보지 않는다', () => {
    const scope = requestScope('svc:blog/dev', data)
    const first = appendLiveSteps([], rows, data, 0, scope)
    expect(first.steps.map((step) => step.id)).toEqual(['2'])
    expect(first.lastID).toBe(4)
    const second = appendLiveSteps(first.steps, [...rows, request(5, '172.20.0.3:8080')], data, first.lastID, scope)
    expect(second.steps).toBe(first.steps)
    expect(second.lastID).toBe(5)
    expect(liveRequestScript([], false, scope).title).toBe('blog/dev 실시간 요청')
  })
})

describe('실시간 API 요청', () => {
  const request = (id: number): RequestRow => ({
    id, at: new Date(Date.UTC(2026, 10, 6, 3, 0, id)).toISOString(), service: 'nginx', method: 'GET', path: `/p/${id}`, proto: 'HTTP/1.1', status: 200, bytes: 10, duration_ms: 5, upstream: '', upstream_ms: null,
  })
  const data = topology([service('blog/api')], [], [nginx])

  it('본 요청 뒤에 들어온 것만 순서대로 덧붙인다', () => {
    const first = appendLiveSteps([], [request(3), request(2), request(1)], data, 1)
    expect(first.steps.map((step) => step.id)).toEqual(['2', '3'])
    expect(first.lastID).toBe(3)
    const second = appendLiveSteps(first.steps, [request(4), request(3), request(2)], data, first.lastID)
    expect(second.steps.map((step) => step.id)).toEqual(['2', '3', '4'])
    expect(second.dropped).toBe(0)
  })

  it('새 요청이 없으면 그대로 둔다', () => {
    const result = appendLiveSteps([], [request(1)], data, 5)
    expect(result).toEqual({ steps: [], dropped: 0, lastID: 5 })
  })

  it('넘치면 오래된 것부터 빼고 뺀 수를 알려준다', () => {
    const many = Array.from({ length: LIVE_MAX_STEPS + 4 }, (_, index) => request(index + 1))
    const result = appendLiveSteps([], many, data, 0)
    expect(result.steps).toHaveLength(LIVE_MAX_STEPS)
    expect(result.dropped).toBe(4)
    expect(result.steps[0].id).toBe('5')
  })

  it('요청이 아직 없어도 시간표가 깨지지 않는다', () => {
    const timeline = buildTimeline(liveRequestScript([]))
    expect(timeline.total).toBe(0)
    expect(frameAt(timeline, 0).delivered).toBe(0)
    expect(hopProgress(timeline, 0)).toBe(1)
  })

  it('앞에 덧붙여도 지난 구간의 시각은 그대로다', () => {
    const one = buildTimeline(liveRequestScript(appendLiveSteps([], [request(1)], data, 0).steps))
    const two = buildTimeline(liveRequestScript(appendLiveSteps(appendLiveSteps([], [request(1)], data, 0).steps, [request(2)], data, 1).steps))
    expect(two.stepStarts[0]).toBe(one.stepStarts[0])
    expect(two.stepStarts[1]).toBe(one.total)
  })

  it('데모와 실제를 구분해 말한다', () => {
    expect(liveRequestScript([], true).note).toContain('가짜')
    expect(liveRequestScript([]).note).toContain('늦게')
  })
})
