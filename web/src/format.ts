import type { EventRow, LatestValue, PortBinding } from './api'

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

export interface PortGroup {
  loopback: boolean
  hostFrom: number
  hostTo: number
  containerFrom: number
  containerTo: number
  proto: string
}

const isLoopbackAddress = (host?: string) => host === '127.0.0.1' || host === '::1'

// groupPorts는 공개된 포트를 묶는다. IPv4와 IPv6 바인딩은 하나로 보고 연속된 포트는 범위로 합친다.
export function groupPorts(ports: PortBinding[]): PortGroup[] {
  const unique = new Map<string, PortGroup>()
  for (const port of ports) {
    if (!port.host_port) continue
    const loopback = isLoopbackAddress(port.host_ip)
    const key = `${loopback}|${port.proto}|${port.host_port}|${port.container_port}`
    unique.set(key, {
      loopback,
      proto: port.proto,
      hostFrom: port.host_port,
      hostTo: port.host_port,
      containerFrom: port.container_port,
      containerTo: port.container_port,
    })
  }
  const sorted = [...unique.values()].sort((a, b) => a.hostFrom - b.hostFrom)
  const groups: PortGroup[] = []
  for (const port of sorted) {
    const last = groups.at(-1)
    if (
      last &&
      last.loopback === port.loopback &&
      last.proto === port.proto &&
      last.hostTo + 1 === port.hostFrom &&
      last.containerTo + 1 === port.containerFrom
    ) {
      last.hostTo = port.hostTo
      last.containerTo = port.containerTo
    } else {
      groups.push({ ...port })
    }
  }
  return groups
}

const range = (from: number, to: number) => (from === to ? String(from) : `${from}-${to}`)

export function formatPortGroup(group: PortGroup): string {
  const host = range(group.hostFrom, group.hostTo)
  const container = range(group.containerFrom, group.containerTo)
  const prefix = group.loopback ? '127.0.0.1:' : ''
  return group.hostFrom === group.containerFrom ? `${prefix}${host}` : `${prefix}${host}→${container}`
}

// summarizePorts는 좁은 라벨에 맞게 앞의 몇 개만 보여주고 나머지는 개수로 남긴다.
export function summarizePorts(ports: PortBinding[], limit = 3): string {
  const groups = groupPorts(ports)
  const shown = groups.slice(0, limit).map(formatPortGroup).join(', ')
  return groups.length > limit ? `${shown} 외 ${groups.length - limit}개` : shown
}

const COMPACT_PORT_CHARS = 18

// compactPorts는 장비 라벨용이다. 길면 첫 묶음만 보여주고 전체는 상세 패널에서 본다.
export function compactPorts(ports: PortBinding[]): string {
  const two = summarizePorts(ports, 2)
  return two.length <= COMPACT_PORT_CHARS ? two : summarizePorts(ports, 1)
}
