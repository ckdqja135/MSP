import { describe, expect, it } from 'vitest'
import type { Container, HostService, Probe, Project, Relation, Service, Topology } from './api'
import { buildTimeline, frameAt, hopProgress, nextArrival, observedScript, overviewFrame, previousStepStart, simulationScript } from './flow'

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
