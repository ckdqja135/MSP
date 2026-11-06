import { describe, expect, it } from 'vitest'
import type { Container, EventRow, Probe, Service, Topology } from './api'
import { diffActivities, eventActivity, mergeCallouts, type Activity } from './activity'

const container = (id: string): Container => ({ id, name: 'c', image: 'x', state: 'running', exit_code: 0, health: 'none', restart_count: 0, created_at: '', started_at: '', replica: 1, project: 'blog', service: 'api', metrics: {} })
const probe = (overrides: Partial<Probe> = {}): Probe => ({ vantage: 'internal', target: 'GET /health', state: 'ok', failures: 0, last_checked: 't1', latency_ms: 12, ...overrides })
const service = (overrides: Partial<Service> = {}): Service => ({ key: 'blog/api', name: 'api', project: 'blog', status: 'ok', runtime: 'running', response: 'ok', reasons: [], containers: [container('aaa')], probes: [], ...overrides })
const topology = (services: Service[], historical = false): Topology => ({ server_id: 's', version_id: 1, valid_from: '', valid_to: null, observed_at: '', historical, projects: [{ key: 'blog', status: 'ok', services }], networks: [], relations: [] })

describe('diffActivities', () => {
  it('변화가 없으면 아무것도 그리지 않는다', () => {
    const data = topology([service({ probes: [probe()] })])
    expect(diffActivities(data, data, 0)).toEqual([])
  })

  it('응답 확인이 실제로 실행됐을 때만 경로를 그린다', () => {
    const [activity] = diffActivities(topology([service({ probes: [probe()] })]), topology([service({ probes: [probe({ last_checked: 't2' })] })]), 0)
    expect(activity.from).toBe('host')
    expect(activity.to).toBe('svc:blog/api')
    expect(activity.tone).toBe('ok')
    expect(activity.quiet).toBe(true)
  })

  it('실패한 응답 확인은 카드로 알리고 측정값을 꾸며내지 않는다', () => {
    const failed = probe({ vantage: 'external', target: 'GET https://blog.example.test/api/health', last_checked: 't2', state: 'failing', failures: 1, latency_ms: null })
    const [activity] = diffActivities(topology([service({ probes: [probe({ vantage: 'external' })] })]), topology([service({ probes: [failed] })]), 0)
    expect(activity.from).toBe('internet')
    expect(activity.tone).toBe('fail')
    expect(activity.quiet).toBeFalsy()
    expect(activity.title).toBe('GET /api/health')
    expect(activity.lines[0]).toContain('응답 없음')
    expect(activity.lines.join()).not.toMatch(/\d+ms/)
  })

  it('재생성은 교체로 알리고 새 연결선을 꾸며내지 않는다', () => {
    const [activity] = diffActivities(topology([service()]), topology([service({ containers: [container('bbb')] })]), 0)
    expect(activity.title).toBe('컨테이너 교체')
    expect(activity.from).toBeUndefined()
  })

  it('새 서비스를 알린다', () => {
    const added = service({ key: 'blog/web', name: 'web' })
    expect(diffActivities(topology([service()]), topology([service(), added]), 0).map((item) => item.title)).toEqual(['새 서비스'])
  })

  it('과거 시점 화면에서는 그리지 않는다', () => {
    expect(diffActivities(topology([service()]), topology([service({ containers: [container('bbb')] })], true), 0)).toEqual([])
  })
})

describe('eventActivity', () => {
  const event = (overrides: Partial<EventRow>): EventRow => ({ id: 1, occurred_at: '', received_at: '', category: 'ops', type: 'container.oom', severity: 'critical', source: 'docker', target: 'blog-api-1', service_key: 'blog/api', container_id: '', summary: 'OOM', duration_ms: null, duration_limit_ms: null, bytes: null, result: '', evidence: '', aggregated: 0, ...overrides })

  it('Docker 기록은 경로 없이 대상에만 표시한다', () => {
    const activity = eventActivity(event({}))
    expect(activity?.from).toBeUndefined()
    expect(activity?.to).toBe('svc:blog/api')
    expect(activity?.lines.join()).not.toContain('소요')
  })

  it('프로브 기록은 실제 요청 경로를 그리고 제한 초과를 그대로 표시한다', () => {
    const activity = eventActivity(event({ category: 'probe', type: 'probe.failed', source: 'probe:external', duration_limit_ms: 5000 }))
    expect(activity?.from).toBe('internet')
    expect(activity?.lines.join()).toContain('>5,000ms (제한 초과)')
  })

  it('서비스가 없는 기록은 그리지 않는다', () => {
    expect(eventActivity(event({ service_key: '' }))).toBeUndefined()
  })
})

describe('mergeCallouts', () => {
  const activity = (overrides: Partial<Activity>): Activity => ({ id: 'a', to: 'svc:x', title: 't', lines: ['l'], tone: 'info', until: 10, ...overrides })

  it('같은 대상의 카드는 한 장으로 합치고 실패를 앞에 둔다', () => {
    const merged = mergeCallouts([
      activity({ id: '1', title: '상태 변경', lines: ['변경'] }),
      activity({ id: '2', title: '응답 실패', lines: ['내부 프로브'], tone: 'fail' }),
    ])
    expect(merged).toHaveLength(1)
    expect(merged[0].title).toBe('응답 실패')
    expect(merged[0].lines).toEqual(['내부 프로브', '변경'])
  })

  it('다른 대상은 나누고 조용한 활동은 카드로 만들지 않는다', () => {
    const merged = mergeCallouts([activity({ id: '1' }), activity({ id: '2', to: 'svc:y' }), activity({ id: '3', to: 'svc:z', quiet: true })])
    expect(merged.map((item) => item.to)).toEqual(['svc:x', 'svc:y'])
  })

  it('선택한 기록은 다른 활동과 합치지 않는다', () => {
    const merged = mergeCallouts([
      activity({ id: '1', tone: 'fail', lines: ['실패'] }),
      activity({ id: '3', title: '선택', lines: ['a', 'b', 'c'], until: Number.POSITIVE_INFINITY }),
    ])
    expect(merged[0].title).toBe('선택')
    expect(merged[0].lines).toEqual(['a', 'b', 'c'])
  })

  it('합칠 때 줄 수를 제한하고 나머지는 건수로 남긴다', () => {
    const merged = mergeCallouts(['1', '2', '3', '4', '5'].map((id) => activity({ id, lines: [`줄${id}`] })))
    expect(merged[0].lines).toEqual(['줄1', '줄2', '외 3건'])
  })
})
