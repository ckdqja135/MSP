import { useMemo } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { fetchEvents, fetchGaps, type EventRow, type GapRow, type Topology } from './api'
import { parseTarget, timeMode, timeWindow, type ConsoleContext } from './context'

const PAGE_SIZE = 200

// targetServices는 선택 대상을 흐름표 필터로 바꾼다. 서버 전체면 필터가 없다.
export function targetServices(target: string | undefined, topology: Topology | undefined): string[] | undefined {
  const parsed = parseTarget(target)
  if (!parsed || parsed.kind === 'host' || !topology) return undefined
  if (parsed.kind === 'service') return [parsed.key]
  if (parsed.kind === 'network') return topology.networks.find((network) => network.name === parsed.name)?.members ?? []
  return topology.projects.find((project) => project.key === parsed.key)?.services.map((service) => service.key) ?? []
}

export type FlowRecord = { kind: 'event'; time: number; event: EventRow } | { kind: 'gap'; time: number; gap: GapRow }

// mergeRecords는 공백 구간도 기록처럼 시간순으로 끼워 넣는다. 공백이 표에서 숨겨지지 않게 하기 위해서다.
export function mergeRecords(events: EventRow[], gaps: GapRow[]): FlowRecord[] {
  const records: FlowRecord[] = [
    ...events.map((event): FlowRecord => ({ kind: 'event', time: Date.parse(event.occurred_at), event })),
    ...gaps.map((gap): FlowRecord => ({ kind: 'gap', time: Date.parse(gap.to), gap })),
  ]
  return records.sort((a, b) => b.time - a.time)
}

export interface FlowData {
  records: FlowRecord[]
  events: EventRow[]
  gaps: GapRow[]
  isLoading: boolean
  isError: boolean
  hasMore: boolean
  loadMore: () => void
  loadingMore: boolean
}

export function useFlowRecords(server: string | undefined, context: ConsoleContext, topology: Topology | undefined): FlowData {
  const live = timeMode(context) === 'live'
  const services = targetServices(context.target, topology)
  const categories = context.categories ? context.categories.split(',') : undefined
  const errorsOnly = context.errors === 'true'
  // 실시간 조회의 키에는 시각을 넣지 않는다. 시각을 넣으면 매초 새 조회가 된다.
  const rangeKey = live ? context.range : [context.from, context.to, context.frozen, context.range]
  const scope = live ? 'live' : 'fixed'

  const events = useInfiniteQuery({
    queryKey: [scope, 'events', server, rangeKey, services, categories, errorsOnly],
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) =>
      fetchEvents(server!, { ...timeWindow(context, Date.now()), services, categories, errorsOnly, before: pageParam, limit: PAGE_SIZE }),
    getNextPageParam: (last) => (last.length === PAGE_SIZE ? last[last.length - 1].id : undefined),
    enabled: Boolean(server) && (services === undefined || services.length > 0),
    placeholderData: (previous) => previous,
  })

  const gaps = useQuery({
    queryKey: [scope, 'gaps', server, rangeKey],
    queryFn: () => {
      const { from, to } = timeWindow(context, Date.now())
      return fetchGaps(server!, from, to)
    },
    enabled: Boolean(server),
    placeholderData: (previous) => previous,
  })

  const eventRows = useMemo(() => events.data?.pages.flat() ?? [], [events.data])
  const gapRows = useMemo(() => gaps.data ?? [], [gaps.data])
  const records = useMemo(() => mergeRecords(eventRows, gapRows), [eventRows, gapRows])

  return {
    records,
    events: eventRows,
    gaps: gapRows,
    isLoading: events.isLoading,
    isError: events.isError,
    hasMore: events.hasNextPage,
    loadMore: () => events.fetchNextPage(),
    loadingMore: events.isFetchingNextPage,
  }
}
