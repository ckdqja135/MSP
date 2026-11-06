import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchEvents, fetchMeta, fetchServers, fetchTopology } from './api'
import { BottomPanel } from './components/BottomPanel'
import { TopBar } from './components/TopBar'
import { Explorer } from './components/Explorer'
import { DetailPanel } from './components/DetailPanel'
import { TopologyMap, type MapHighlight } from './components/TopologyMap'
import { timeMode, updateContext, useConsoleContext } from './context'
import { formatDateTime } from './format'
import { useFlowRecords } from './useFlowRecords'

const REFRESH_INTERVAL_MS = 3000

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}

// useLiveRefresh는 변경 신호를 받아 다시 조회한다. 화면 고정 중에는 조회만 멈추고 수집에는 영향을 주지 않는다.
function useLiveRefresh(paused: boolean): boolean {
  const queryClient = useQueryClient()
  const [connected, setConnected] = useState(true)
  const lastRefresh = useRef(0)
  const pending = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const stream = new EventSource('/api/v1/stream')
    stream.onopen = () => setConnected(true)
    stream.onerror = () => setConnected(false)
    stream.onmessage = () => {
      if (paused || pending.current) return
      const wait = Math.max(0, lastRefresh.current + REFRESH_INTERVAL_MS - Date.now())
      pending.current = setTimeout(() => {
        pending.current = null
        lastRefresh.current = Date.now()
        queryClient.invalidateQueries({ queryKey: ['live'] })
      }, wait)
    }
    return () => {
      stream.close()
      if (pending.current) clearTimeout(pending.current)
      pending.current = null
    }
  }, [paused, queryClient])
  return connected
}

export function App() {
  const context = useConsoleContext()
  const mode = timeMode(context)
  const live = mode === 'live'
  const now = useNow(1000)
  const streamConnected = useLiveRefresh(!live)

  const meta = useQuery({ queryKey: ['meta'], queryFn: fetchMeta, staleTime: Infinity })
  const servers = useQuery({ queryKey: ['live', 'servers'], queryFn: fetchServers, enabled: live || !meta.isSuccess })

  const server = servers.data?.find((item) => item.id === context.server) ?? servers.data?.[0]
  useEffect(() => {
    if (server && context.server !== server.id) updateContext({ server: server.id })
  }, [server, context.server])

  const historicalAt = mode === 'past' ? (context.at ?? context.to) : undefined
  const topology = useQuery({
    queryKey: [live ? 'live' : 'fixed', 'topology', server?.id, historicalAt],
    queryFn: () => fetchTopology(server!.id, historicalAt),
    enabled: Boolean(server),
    placeholderData: (previous) => previous,
    staleTime: live ? 0 : Infinity,
  })

  const clock = mode === 'frozen' && context.frozen ? Date.parse(context.frozen) : now
  const flow = useFlowRecords(server?.id, context, topology.data)
  const selectedEvent = flow.events.find((event) => String(event.id) === context.event)

  // 행 선택은 강조만 하고 대상 필터는 바꾸지 않는다. 필터를 바꾸면 선택한 행이 표에서 사라질 수 있다.
  const highlight = useMemo<MapHighlight>(() => {
    if (!selectedEvent?.service_key) return { nodes: new Set() }
    return {
      nodes: new Set([`svc:${selectedEvent.service_key}`]),
      note: '선택한 기록의 대상만 강조합니다. 시간이 가깝다는 이유로 원인으로 판단하지 않습니다.',
    }
  }, [selectedEvent])

  const pendingSinceFreeze = useQuery({
    queryKey: ['frozen-count', server?.id, context.frozen],
    queryFn: () => fetchEvents(server!.id, { from: context.frozen!, to: new Date().toISOString(), limit: 500 }),
    enabled: mode === 'frozen' && Boolean(server),
    refetchInterval: 5000,
  })

  return (
    <div className="console">
      <TopBar
        meta={meta.data}
        servers={servers.data ?? []}
        server={server}
        topology={topology.data}
        context={context}
        now={now}
        streamConnected={streamConnected}
      />
      {meta.data?.mode === 'demo' && (
        <div className="demo-banner" role="status">
          데모 화면입니다. 표시되는 서버, 컨테이너, 이벤트, 지표는 모두 가짜 데이터입니다.
        </div>
      )}
      {mode === 'frozen' && (
        <div className="freeze-banner" role="status">
          화면 갱신만 멈췄습니다. 서버와 데이터 수집은 계속 동작합니다.
          <span>신규 기록 {pendingSinceFreeze.data?.length ?? 0}{pendingSinceFreeze.data?.length === 500 ? '건 이상' : '건'} 대기</span>
          <button type="button" className="link" onClick={() => updateContext({ frozen: undefined })}>실시간으로 돌아가기</button>
        </div>
      )}
      {mode === 'past' && (
        <div className="freeze-banner" role="status">
          과거 구간을 보고 있습니다. 구조맵은 {historicalAt ? formatDateTime(historicalAt) : ''} 시점입니다.
          <button type="button" className="link" onClick={() => updateContext({ from: undefined, to: undefined, at: undefined })}>실시간으로 돌아가기</button>
        </div>
      )}
      <main className="workspace">
        <Explorer server={server} topology={topology.data} target={context.target} />
        <section className="map-surface" aria-label="서버와 컨테이너 구조맵">
          {servers.isError && <p className="empty">중앙 서버에서 데이터를 받지 못했습니다.</p>}
          {servers.data?.length === 0 && <p className="empty">등록된 서버가 없습니다. 아직 수신한 데이터가 없습니다.</p>}
          {topology.isError && server && <p className="empty">구조 데이터를 아직 받지 못했습니다.</p>}
          {server && topology.data && (
            <TopologyMap key={server.id} server={server} topology={topology.data} target={context.target} highlight={highlight} now={clock} />
          )}
        </section>
        <DetailPanel meta={meta.data} server={server} topology={topology.data} target={context.target} now={clock} selectedEvent={selectedEvent} events={flow.events} />
        <BottomPanel meta={meta.data} context={context} flow={flow} selectedID={context.event} server={server} topology={topology.data} selectedEvent={selectedEvent} now={clock} />
      </main>
    </div>
  )
}
