import type { EventRow, RequestRow, Service, Topology } from './api'
import { entryDevice, upstreamDevice } from './flow'
import { formatDuration, shortID } from './format'
import { serviceHeadline, serviceLabel, sourceLabel, typeLabel } from './status'

// Activity는 구조도에 잠깐 그리는 실제 사건이다. 관측된 변화에서만 만들어진다.
export interface Activity {
  id: string
  from?: string
  to: string
  title: string
  lines: string[]
  tone: 'ok' | 'fail' | 'info'
  // 정상 응답은 경로만 잠깐 그리고 설명 카드는 띄우지 않는다.
  quiet?: boolean
  // 실제 API 요청의 경로는 응답 확인과 다른 색으로 그린다.
  request?: boolean
  until: number
}

const PROBE_MS = 2200
const REQUEST_MS = 2200
const CHANGE_MS = 7000

function indexServices(topology: Topology): Map<string, Service> {
  const services = new Map<string, Service>()
  for (const project of topology.projects) {
    for (const service of project.services) services.set(service.key, service)
  }
  return services
}

// diffActivities는 이전 구조와 현재 구조를 비교해 새로 일어난 일을 찾는다.
export function diffActivities(previous: Topology, next: Topology, now: number): Activity[] {
  if (next.historical || previous.historical) return []
  const before = indexServices(previous)
  const activities: Activity[] = []

  for (const service of indexServices(next).values()) {
    const to = `svc:${service.key}`
    const old = before.get(service.key)
    if (!old) {
      activities.push({ id: `new:${service.key}:${now}`, to, title: '새 서비스', lines: [serviceLabel(service.key)], tone: 'info', until: now + CHANGE_MS })
      continue
    }

    const oldIDs = old.containers.map((container) => container.id).join()
    const newIDs = service.containers.map((container) => container.id).join()
    if (oldIDs !== newIDs) {
      activities.push({
        id: `replace:${service.key}:${now}`,
        to,
        title: '컨테이너 교체',
        lines: [`${shortID(old.containers[0]?.id ?? '')} → ${shortID(service.containers[0]?.id ?? '')}`, '같은 서비스로 이력을 잇습니다'],
        tone: 'info',
        until: now + CHANGE_MS,
      })
    } else if (old.runtime !== service.runtime || old.response !== service.response) {
      const fail = service.status === 'critical'
      activities.push({ id: `state:${service.key}:${now}`, to, title: '상태 변경', lines: [serviceHeadline(service)], tone: fail ? 'fail' : 'info', until: now + CHANGE_MS })
    }

    for (const probe of service.probes) {
      const last = old.probes.find((item) => item.vantage === probe.vantage)
      if (!last || last.last_checked === probe.last_checked) continue
      const ok = probe.state === 'ok'
      const vantage = probe.vantage === 'external' ? '외부 프로브' : '내부 프로브'
      activities.push({
        id: `probe:${service.key}:${probe.vantage}:${probe.last_checked}`,
        from: probe.vantage === 'external' ? 'internet' : 'host',
        to,
        title: probe.target.replace(/^GET https?:\/\/[^/]+/, 'GET '),
        lines: [`${vantage} · ${probe.latency_ms !== null ? `${probe.latency_ms}ms` : '응답 없음'}`, ...(ok ? [] : [`연속 실패 ${probe.failures}회`])],
        tone: ok ? 'ok' : 'fail',
        quiet: ok,
        until: now + PROBE_MS,
      })
    }
  }
  return activities
}

// eventActivity는 흐름표에서 고른 기록을 구조도에 그린다. 프로브처럼 실제 요청이 있었던 기록만 경로를 그린다.
export function eventActivity(event: EventRow): Activity | undefined {
  if (!event.service_key) return undefined
  const from = event.source === 'probe:external' ? 'internet' : event.source === 'probe:internal' ? 'host' : undefined
  const duration = formatDuration(event)
  return {
    id: `event:${event.id}`,
    from,
    to: `svc:${event.service_key}`,
    title: typeLabel(event.type),
    lines: [sourceLabel(event.source), event.summary, ...(duration === '—' ? [] : [`소요 ${duration}`])],
    tone: event.severity === 'info' ? 'info' : 'fail',
    until: Number.POSITIVE_INFINITY,
  }
}

// requestActivities는 새로 들어온 요청을 구조도에 그린다. 같은 경로는 한 번만 그려 요청이 몰려도 화면이 덮이지 않게 한다.
export function requestActivities(requests: RequestRow[], topology: Topology, now: number): Activity[] {
  const routes = new Map<string, { from: string; to: string; rows: RequestRow[] }>()
  const add = (from: string, to: string, request: RequestRow) => {
    const key = `${from}>${to}`
    const route = routes.get(key) ?? { from, to, rows: [] }
    route.rows.push(request)
    routes.set(key, route)
  }
  for (const request of requests) {
    const entry = entryDevice(request.service, topology)
    add('internet', entry, request)
    const target = upstreamDevice(request.upstream, topology)
    if (target && entry !== 'host') add(entry, target.id, request)
  }
  return [...routes.values()].map(({ from, to, rows }) => {
    const latest = rows[rows.length - 1]
    const failures = rows.filter((row) => row.status >= 500)
    const shown = failures[failures.length - 1] ?? latest
    const duration = shown.duration_ms !== null ? ` · ${shown.duration_ms}ms` : ''
    return {
      id: `request:${from}:${to}:${now}`,
      from,
      to,
      title: `${shown.method} ${shown.path}`,
      lines: [`${shown.status}${duration}`, ...(rows.length > 1 ? [`이 경로로 ${rows.length}건`] : [])],
      tone: failures.length > 0 ? 'fail' : 'ok',
      quiet: failures.length === 0,
      request: true,
      until: now + REQUEST_MS,
    }
  })
}

const MAX_CARD_LINES = 3

function priority(activity: Activity): number {
  if (activity.until === Number.POSITIVE_INFINITY) return 0
  if (activity.tone === 'fail') return 1
  return 2
}

// mergeCallouts는 대상별로 카드를 한 장으로 합친다. 카드가 쌓여 다른 장비를 가리지 않게 하기 위해서다.
export function mergeCallouts(activities: Activity[]): Activity[] {
  const byTarget = new Map<string, Activity[]>()
  for (const activity of activities) {
    if (activity.quiet) continue
    byTarget.set(activity.to, [...(byTarget.get(activity.to) ?? []), activity])
  }
  return [...byTarget.values()].map((group) => {
    const [first, ...others] = [...group].sort((a, b) => priority(a) - priority(b))
    // 사용자가 고른 기록은 내용을 그대로 보여준다.
    if (first.until === Number.POSITIVE_INFINITY) return first
    const lines = [...first.lines, ...others.map((other) => other.lines[0])]
    const shownLines = lines.slice(0, MAX_CARD_LINES)
    if (lines.length > MAX_CARD_LINES) shownLines[MAX_CARD_LINES - 1] = `외 ${lines.length - MAX_CARD_LINES + 1}건`
    return { ...first, lines: shownLines }
  })
}
