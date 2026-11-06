import { useSyncExternalStore } from 'react'

// 선택 대상과 시간 범위는 URL에 저장한다. 새로고침과 알림 링크가 같은 화면을 연다.
export interface ConsoleContext {
  server?: string
  target?: string
  range: string
  from?: string
  to?: string
  frozen?: string
  event?: string
  tab: string
}

export const RANGES: Record<string, number> = {
  '15m': 15 * 60_000,
  '1h': 60 * 60_000,
  '6h': 6 * 60 * 60_000,
  '24h': 24 * 60 * 60_000,
}

const CHANGE = 'msp:context'

export function parseContext(search: string): ConsoleContext {
  const params = new URLSearchParams(search)
  const range = params.get('range') ?? '1h'
  return {
    server: params.get('server') ?? undefined,
    target: params.get('target') ?? undefined,
    range: range in RANGES ? range : '1h',
    from: params.get('from') ?? undefined,
    to: params.get('to') ?? undefined,
    frozen: params.get('frozen') ?? undefined,
    event: params.get('event') ?? undefined,
    tab: params.get('tab') ?? 'flow',
  }
}

export function serializeContext(context: ConsoleContext): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(context)) {
    if (value !== undefined && value !== '') params.set(key, value)
  }
  return params.toString()
}

let cachedSearch = ''
let cached: ConsoleContext = parseContext('')

function snapshot(): ConsoleContext {
  if (window.location.search !== cachedSearch) {
    cachedSearch = window.location.search
    cached = parseContext(cachedSearch)
  }
  return cached
}

function subscribe(listener: () => void) {
  window.addEventListener('popstate', listener)
  window.addEventListener(CHANGE, listener)
  return () => {
    window.removeEventListener('popstate', listener)
    window.removeEventListener(CHANGE, listener)
  }
}

export function useConsoleContext(): ConsoleContext {
  return useSyncExternalStore(subscribe, snapshot)
}

export function updateContext(patch: Partial<ConsoleContext>) {
  const next = { ...snapshot(), ...patch }
  const query = serializeContext(next)
  window.history.pushState(null, '', query ? `?${query}` : window.location.pathname)
  window.dispatchEvent(new Event(CHANGE))
}

export type TimeMode = 'live' | 'frozen' | 'past'

export function timeMode(context: ConsoleContext): TimeMode {
  if (context.from && context.to) return 'past'
  if (context.frozen) return 'frozen'
  return 'live'
}

// timeWindow는 패널들이 함께 쓰는 조회 구간을 계산한다.
export function timeWindow(context: ConsoleContext, now: number): { from: string; to: string } {
  if (context.from && context.to) return { from: context.from, to: context.to }
  const end = context.frozen ? Date.parse(context.frozen) : now
  return {
    from: new Date(end - RANGES[context.range]).toISOString(),
    to: new Date(end).toISOString(),
  }
}

export type Target =
  | { kind: 'host' }
  | { kind: 'service'; key: string }
  | { kind: 'network'; name: string }
  | { kind: 'project'; key: string }

export function parseTarget(value?: string): Target | undefined {
  if (!value) return undefined
  if (value === 'host') return { kind: 'host' }
  const [kind, rest] = [value.slice(0, value.indexOf(':')), value.slice(value.indexOf(':') + 1)]
  if (kind === 'svc') return { kind: 'service', key: rest }
  if (kind === 'net') return { kind: 'network', name: rest }
  if (kind === 'project') return { kind: 'project', key: rest }
  return undefined
}
