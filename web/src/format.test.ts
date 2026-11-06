import { describe, expect, it } from 'vitest'
import { formatBytes, formatRate, readMetric } from './format'

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
