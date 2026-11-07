import { describe, expect, it } from 'vitest'
import type { EventRow, GapRow } from './api'
import type { FlowPacket, FlowScript } from './flow'
import { cleanText, containerLog, eventLine, eventsLog, logFileName, logTime, metricsLog, playbackLog } from './logExport'

const NOW = Date.parse('2026-11-06T03:00:00Z')
const event = (overrides: Partial<EventRow> = {}): EventRow => ({
  id: 1, occurred_at: '2026-11-06T02:00:00Z', received_at: '2026-11-06T02:00:01Z', category: 'probe', type: 'probe.failed', severity: 'warning', source: 'probe:internal', target: 'blog/api', service_key: 'blog/api', container_id: '', summary: 'GET /health 실패', duration_ms: null, duration_limit_ms: null, bytes: null, result: '실패 1/3', evidence: 'probe', aggregated: 1, ...overrides,
})
const gap = (overrides: Partial<GapRow> = {}): GapRow => ({ source: 'agent', from: '2026-11-06T02:30:00Z', to: '2026-11-06T02:31:00Z', reason: '수신 없음', dropped: 2, ...overrides }) as GapRow
const header = { server: 'home-1', demo: false }

describe('시각', () => {
  it('시간대가 드러나고 같은 시점으로 되돌아온다', () => {
    const text = logTime('2026-11-06T02:00:00Z')
    expect(text).toMatch(/^2026-11-0\dT\d\d:\d\d:\d\d[+-]\d\d:\d\d$/)
    expect(Date.parse(text)).toBe(Date.parse('2026-11-06T02:00:00Z'))
  })

  it('읽을 수 없거나 비어 있는 시각은 지어내지 않는다', () => {
    expect(logTime('없음')).toBe('-')
    expect(logTime('0001-01-01T00:00:00Z')).toBe('-')
  })
})

describe('한 줄 유지', () => {
  it('줄바꿈과 제어 문자가 다른 줄처럼 보이지 않는다', () => {
    expect(cleanText('a\nb\r\nc')).toBe('a\\nb\\nc')
    expect(cleanText('\u001b[31mred')).toBe('\\x1b[31mred')
  })

  it('이벤트 내용의 줄바꿈으로 가짜 기록을 끼워 넣을 수 없다', () => {
    const line = eventLine(event({ summary: 'ok\n2026-01-01 CRIT 가짜 "기록"' }))
    expect(line).not.toContain('\n')
    expect(line).toContain('msg="ok\\n2026-01-01 CRIT 가짜 \\"기록\\""')
  })
})

describe('이벤트', () => {
  it('측정하지 않은 값은 비워 둔다', () => {
    const line = eventLine(event())
    expect(line).toContain('duration_ms=-')
    expect(line).toContain('bytes=-')
  })

  it('0은 측정값으로 남는다', () => {
    expect(eventLine(event({ duration_ms: 0, bytes: 0 }))).toContain('duration_ms=0')
  })

  it('제한 시간 초과를 정확한 소요처럼 적지 않는다', () => {
    expect(eventLine(event({ duration_limit_ms: 5000 }))).toContain('duration_over_ms=5000')
  })

  it('오래된 순서로 적고 공백을 끼워 넣는다', () => {
    const file = eventsLog([event({ id: 2, occurred_at: '2026-11-06T02:40:00Z' }), event({ id: 1 })], [gap()], header, false, NOW)
    const body = file.text.split('\n').filter((line) => line && !line.startsWith('#'))
    expect(body).toHaveLength(3)
    expect(body[1]).toContain(' GAP ')
    expect(file.count).toBe(3)
    expect(file.text).toContain('# 건수: 3')
    expect(file.name).toMatch(/^msp-home-1-events-\d{8}-\d{6}\.log$/)
  })

  it('일부만 담았으면 파일에 밝힌다', () => {
    expect(eventsLog([event()], [], header, true, NOW).text).toContain('최근 1건까지만')
  })
})

describe('데모 구분', () => {
  it('데모 파일은 머리글과 이름에 드러난다', () => {
    const file = eventsLog([event()], [], { ...header, demo: true }, false, NOW)
    expect(file.text).toContain('데모용 가짜 데이터')
    expect(file.name).toContain('-demo-')
  })

  it('실제 파일에는 데모 표시가 없다', () => {
    expect(eventsLog([event()], [], header, false, NOW).text).not.toContain('데모')
  })
})

describe('통신 재생', () => {
  const packet: FlowPacket = { id: 'a', kind: 'request', protocol: 'HTTP', title: 'GET /', detail: '외부 → web', path: ['internet', 'svc:a'], from: '외부', to: 'blog/web', at: null, durationMS: null, bytes: null, result: '예시' }
  const script: FlowScript = { source: 'simulation', title: 'blog/web로 들어오는 요청', note: '실제 통신이 아닙니다.', steps: [{ id: 's', title: '요청', summary: '', description: '', packets: [packet] }], facts: [] }

  it('시뮬레이션은 파일에서도 실제가 아님을 밝히고 시각을 비운다', () => {
    const file = playbackLog(script, 'home-1', false, NOW)
    expect(file.text).toContain('시뮬레이션')
    expect(file.text).toContain('실제 통신이 아닙니다')
    expect(file.name).toContain('simulation')
    expect(file.text).toContain('\n- 요청 no=1 ')
    expect(file.text).toContain('duration_ms=- bytes=- result=예시')
  })

  it('API 요청은 크기까지 적고 이름으로 구분된다', () => {
    const requests: FlowScript = { ...script, source: 'requests', steps: [{ ...script.steps[0], packets: [{ ...packet, kind: 'response', at: '2026-11-06T02:00:00Z', durationMS: 12, bytes: 1500, result: '200' }] }] }
    const file = playbackLog(requests, 'home-1', false, NOW)
    expect(file.text).toContain('duration_ms=12 bytes=1500 result=200')
    expect(file.name).toContain('-playback-requests-')
    expect(file.text).toContain('API 요청')
  })

  it('기록은 실제 시각과 소요를 적는다', () => {
    const observed: FlowScript = { ...script, source: 'observed', steps: [{ ...script.steps[0], packets: [{ ...packet, kind: 'response', at: '2026-11-06T02:00:00Z', durationMS: 12, result: '정상' }] }] }
    const file = playbackLog(observed, 'home-1', false, NOW)
    expect(file.text).toContain('duration_ms=12')
    expect(file.name).toContain('observed')
  })
})

describe('지표', () => {
  it('값이 있는 칸만 적고 공백을 0으로 채우지 않는다', () => {
    const file = metricsLog([{ scope: 'host', name: 'host.cpu.percent', stepMS: 30000, points: [{ t: NOW - 60000, v: 12.5 }, { t: NOW, v: 0 }] }], [gap()], header, 'host', NOW)
    expect(file.count).toBe(2)
    expect(file.text).toContain('name=host.cpu.percent value=12.5 step_ms=30000')
    expect(file.text).toContain('value=0 ')
    expect(file.text).toContain('수집 공백 1구간')
  })
})

describe('컨테이너 로그', () => {
  it('받아 둔 줄만 담았고 가림이 완전하지 않다고 밝힌다', () => {
    const file = containerLog([{ at: '2026-11-06T02:00:00Z', stream: 'stdout', text: 'password=[가려짐]\u001b[0m' }], header, 'blog/api', '조회 완료', 3, NOW)
    expect(file.text).toContain('stdout password=[가려짐]\\x1b[0m')
    expect(file.text).toContain('전부 잡지는 못합니다')
    expect(file.text).toContain('3줄 누락')
    expect(file.name).toMatch(/^msp-home-1-logs-blog-api-\d{8}-\d{6}\.log$/)
  })

  it('시각 없는 줄을 지어내지 않는다', () => {
    const file = containerLog([{ at: '0001-01-01T00:00:00Z', stream: 'stdout', text: 'x' }], header, 'a/b', '조회 완료', 0, NOW)
    expect(file.text).toContain('\n- stdout x')
  })
})

describe('파일 이름', () => {
  it('경로로 쓰일 수 있는 글자를 지운다', () => {
    expect(logFileName('../../etc', ['a b/c'], false, NOW)).toMatch(/^msp-\.\.-\.\.-etc-a-b-c-\d{8}-\d{6}\.log$/)
    expect(logFileName('../../etc', [], false, NOW)).not.toContain('/')
  })
})
