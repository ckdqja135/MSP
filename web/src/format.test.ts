import { describe, expect, it } from 'vitest'
import { compactPorts, formatBytes, formatDuration, formatPortGroup, formatRate, formatSize, groupPorts, isLateArrival, readMetric, summarizePorts } from './format'

describe('readMetric', () => {
  const now = Date.parse('2026-11-06T03:00:00Z')
  const fresh = '2026-11-06T02:59:50Z'
  const stale = '2026-11-06T02:50:00Z'

  it('정상적인 0은 값으로 표시한다', () => {
    expect(readMetric({ value: 0, at: fresh }, now, formatRate)).toEqual({ state: 'fresh', text: '0 B/s' })
  })

  it('값이 없으면 0이 아니라 데이터 없음으로 표시한다', () => {
    expect(readMetric(undefined, now, formatRate)).toEqual({ state: 'missing', text: '데이터 없음' })
  })

  it('오래된 값은 지연으로 표시한다', () => {
    expect(readMetric({ value: 5, at: stale }, now, String)).toEqual({ state: 'stale', text: '5', age: '10분 전' })
  })
})

describe('formatBytes', () => {
  it('단위를 바꾼다', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(1500)).toBe('1.5 KB')
    expect(formatBytes(410_000_000)).toBe('410 MB')
  })
})

describe('formatDuration', () => {
  it('제한 시간 초과를 측정값처럼 표시하지 않는다', () => {
    expect(formatDuration({ category: 'probe', duration_ms: null, duration_limit_ms: 5000 })).toBe('>5,000ms (제한 초과)')
  })

  it('측정값은 0도 값으로 표시한다', () => {
    expect(formatDuration({ category: 'probe', duration_ms: 0, duration_limit_ms: null })).toBe('0ms')
    expect(formatDuration({ category: 'probe', duration_ms: 8200, duration_limit_ms: null })).toBe('8.2초')
  })

  it('수집하지 않은 값과 해당 없는 값을 구분한다', () => {
    expect(formatDuration({ category: 'probe', duration_ms: null, duration_limit_ms: null })).toBe('미측정')
    expect(formatDuration({ category: 'ops', duration_ms: null, duration_limit_ms: null })).toBe('—')
  })
})

describe('formatSize', () => {
  it('크기를 모르면 0으로 표시하지 않는다', () => {
    expect(formatSize({ bytes: null })).toBe('—')
    expect(formatSize({ bytes: 0 })).toBe('0 B')
  })
})

describe('isLateArrival', () => {
  it('발생과 수신 차이로 지연 도착을 판단한다', () => {
    expect(isLateArrival({ occurred_at: '2026-11-06T03:00:00Z', received_at: '2026-11-06T03:00:10Z' })).toBe(false)
    expect(isLateArrival({ occurred_at: '2026-11-06T03:00:00Z', received_at: '2026-11-06T03:05:00Z' })).toBe(true)
  })
})

describe('포트 묶기', () => {
  const port = (host_port: number, container_port = host_port, host_ip = '0.0.0.0', proto = 'tcp') => ({ host_port, container_port, host_ip, proto })

  it('연속된 포트를 범위로 합치고 IPv4와 IPv6 중복을 없앤다', () => {
    const ports = [3002, 3003, 3004].flatMap((n) => [port(n), port(n, n, '::')])
    expect(groupPorts(ports).map(formatPortGroup)).toEqual(['3002-3004'])
  })

  it('다른 포트로 연결되면 양쪽을 모두 표시한다', () => {
    expect(groupPorts([port(8001, 8000)]).map(formatPortGroup)).toEqual(['8001→8000'])
    expect(groupPorts([port(9000, 80), port(9001, 81)]).map(formatPortGroup)).toEqual(['9000-9001→80-81'])
  })

  it('번호가 이어져도 연결이 어긋나면 합치지 않는다', () => {
    expect(groupPorts([port(8000, 80), port(8001, 90)]).map(formatPortGroup)).toEqual(['8000→80', '8001→90'])
  })

  it('루프백과 공개 바인딩을 합치지 않는다', () => {
    expect(groupPorts([port(18080, 8080, '127.0.0.1'), port(18081, 8081)]).map(formatPortGroup)).toEqual(['127.0.0.1:18080→8080', '18081→8081'])
  })

  it('공개되지 않은 포트는 제외한다', () => {
    expect(groupPorts([{ container_port: 5432, proto: 'tcp' }])).toEqual([])
  })

  it('장비 라벨은 길면 첫 묶음만 보여준다', () => {
    expect(compactPorts([port(80), port(443)])).toBe('80, 443')
    expect(compactPorts([port(3002), port(3003), port(4000), port(4001), port(8002), port(8003), port(9000)])).toBe('3002-3003 외 3개')
  })

  it('많으면 개수로 줄인다', () => {
    const ports = [port(1000), port(2000), port(3000), port(4000), port(5000)]
    expect(summarizePorts(ports, 3)).toBe('1000, 2000, 3000 외 2개')
  })
})
