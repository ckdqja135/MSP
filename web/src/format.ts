import type { EventRow, LatestValue } from './api'

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (Math.abs(value) >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= 1000
    unit++
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1
  return `${value.toFixed(digits)} ${BYTE_UNITS[unit]}`
}

export function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`
}

export function formatPercent(value: number): string {
  return `${value.toFixed(value >= 100 ? 0 : 1)}%`
}

export function formatAge(from: string | number, now: number): string {
  const seconds = Math.max(0, Math.round((now - new Date(from).getTime()) / 1000))
  if (seconds < 60) return `${seconds}초 전`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`
  return `${Math.floor(seconds / 86400)}일 전`
}

export function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}일 ${hours}시간`
  if (hours > 0) return `${hours}시간 ${minutes}분`
  return `${minutes}분`
}

export function formatClock(value: string | number): string {
  const date = new Date(value)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

export function formatDateTime(value: string | number): string {
  const date = new Date(value)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${formatClock(value)}`
}

export function shortID(id: string): string {
  return id.slice(0, 12)
}

const STALE_AFTER_MS = 60_000

export type Metric =
  | { state: 'missing'; text: string }
  | { state: 'stale'; text: string; age: string }
  | { state: 'fresh'; text: string }

// readMetric은 값이 없는 경우와 정상적인 0을 구분해 표시 문구를 만든다.
export function readMetric(
  latest: LatestValue | undefined,
  now: number,
  format: (value: number) => string,
): Metric {
  if (!latest) return { state: 'missing', text: '데이터 없음' }
  const text = format(latest.value)
  if (now - Date.parse(latest.at) > STALE_AFTER_MS) {
    return { state: 'stale', text, age: formatAge(latest.at, now) }
  }
  return { state: 'fresh', text }
}

function formatMilliseconds(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}초`
}

// formatDuration은 측정값, 제한 초과, 수집 안 함, 해당 없음을 구분한다.
// 응답 확인은 소요 시간 개념이 있으므로 값이 없으면 미측정이고, Docker 이벤트에는 소요 시간 개념이 없다.
export function formatDuration(row: Pick<EventRow, 'category' | 'duration_ms' | 'duration_limit_ms'>): string {
  if (row.duration_limit_ms !== null) return `>${row.duration_limit_ms.toLocaleString('en-US')}ms (제한 초과)`
  if (row.duration_ms !== null) return formatMilliseconds(row.duration_ms)
  return row.category === 'probe' ? '미측정' : '—'
}

export function formatSize(row: Pick<EventRow, 'bytes'>): string {
  return row.bytes === null ? '—' : formatBytes(row.bytes)
}

const LATE_AFTER_MS = 60_000

export function isLateArrival(row: Pick<EventRow, 'occurred_at' | 'received_at'>): boolean {
  return Date.parse(row.received_at) - Date.parse(row.occurred_at) > LATE_AFTER_MS
}
