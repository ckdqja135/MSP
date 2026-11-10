import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchEvents, fetchMeta, fetchRequests, fetchServers, fetchTopology, type RequestRow } from './api'
import { BottomPanel } from './components/BottomPanel'
import { TopBar } from './components/TopBar'
import { Explorer } from './components/Explorer'
import { DetailPanel } from './components/DetailPanel'
import { FlowPanel, PLAY_LABEL } from './components/FlowPlayback'
import { TopologyMap, type MapHighlight } from './components/TopologyMap'
import { timeMode, timeWindow, updateContext, useConsoleContext } from './context'
import { appendLiveSteps, liveRequestScript, observedScript, requestFilter, requestScope, requestScript, scopedEmptyText, simulationScript, type FlowSource, type FlowStep } from './flow'
import { formatDateTime } from './format'
import { targetLabel } from './status'
import { useFlowPlayer } from './useFlowPlayer'
import { useFlowRecords } from './useFlowRecords'

const REFRESH_INTERVAL_MS = 3000
const REQUEST_LIMIT = 100
// 고른 대상에 닿은 요청은 중앙 서버가 골라 주므로 더 많이 받아도 된다.
const SCOPED_REQUEST_LIMIT = 500
const NO_REQUESTS: RequestRow[] = []

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

  // 데이터가 있으면 API 요청을 먼저 보여주고, 응답 확인, 시뮬레이션 순서로 내려간다.
  const flowTab = context.tab === 'play'
  const demo = meta.data?.mode === 'demo'
  const timeKey = live ? context.range : [context.from, context.to, context.frozen, context.range]
  const requests = useQuery({
    queryKey: [live ? 'live' : 'fixed', 'requests', server?.id, timeKey],
    queryFn: () => {
      const { from, to } = timeWindow(context, Date.now())
      return fetchRequests(server!.id, from, to, REQUEST_LIMIT)
    },
    enabled: Boolean(server),
    placeholderData: (previous) => previous,
  })
  const requestRows = requests.data ?? NO_REQUESTS
  // 고른 대상이 있으면 통신 재생도 그 대상에 닿은 요청만 보여준다. 고른 것이 없으면 서버 전체다.
  // 대상에 닿은 요청은 중앙 서버가 구간 전체에서 골라 준다. 최근 요청 몇 건 안에서만 찾으면 바쁜 서버에서 드문 요청을 놓친다.
  // 통신 재생 탭이 아니면 따로 조회하지 않고 최근 요청 안에서 고른다.
  const scopeTarget = targetLabel(context.target) ? context.target : undefined
  const filter = useMemo(() => (flowTab && topology.data ? requestFilter(scopeTarget, topology.data) : undefined), [flowTab, scopeTarget, topology.data])
  const scopedRequests = useQuery({
    queryKey: [live ? 'live' : 'fixed', 'scoped-requests', server?.id, filter, timeKey],
    queryFn: () => {
      const { from, to } = timeWindow(context, Date.now())
      return fetchRequests(server!.id, from, to, SCOPED_REQUEST_LIMIT, filter)
    },
    enabled: Boolean(server && filter && filter.services.length > 0),
  })
  const playQuery = filter ? scopedRequests : requests
  const playRows = filter ? (scopedRequests.data ?? NO_REQUESTS) : requestRows
  const scope = useMemo(() => (topology.data ? requestScope(scopeTarget, topology.data, [...requestRows, ...playRows]) : undefined), [scopeTarget, topology.data, requestRows, playRows])
  const requested = useMemo(() => (topology.data ? requestScript(playRows, topology.data, demo, scope) : undefined), [playRows, topology.data, demo, scope])
  // 새 조회가 끝나기 전에는 앞 조회의 목록이 남아 있으므로 요청이 없다고 말하지 않는다.
  // 구간에 요청이 하나도 없으면 대상 문제가 아니므로 로그 수집을 안내하는 기본 문구를 쓴다.
  let requestEmpty: string | undefined
  if (playQuery.isLoading || playQuery.isPlaceholderData) requestEmpty = '요청을 불러오는 중입니다.'
  else if (playQuery.isError) requestEmpty = '요청 기록을 불러오지 못했습니다.'
  else if (scope && requestRows.length > 0) requestEmpty = scopedEmptyText(scope)
  const observed = useMemo(() => (topology.data ? observedScript(topology.data, scopeTarget, demo) : undefined), [topology.data, scopeTarget, demo])
  const simulation = useMemo(() => (topology.data ? simulationScript(topology.data, scopeTarget) : undefined), [topology.data, scopeTarget])
  const [sourceChoice, setSourceChoice] = useState<FlowSource | undefined>()
  const scripts = { requests: requested, observed, simulation }
  // 시뮬레이션은 직접 골랐을 때만 보여준다. 실제 데이터가 없을 때 예시로 대신하면 실제처럼 보이기 때문이다.
  // 고른 대상에 요청이 없다고 응답 확인으로 넘어가지 않는다. 없다는 안내를 보여준다.
  const source: FlowSource = sourceChoice ?? (requestRows.length === 0 && observed ? 'observed' : 'requests')
  const player = useFlowPlayer(scripts[source])
  const resetPlayer = player.reset
  useEffect(() => {
    if (!flowTab) resetPlayer()
  }, [flowTab, resetPlayer])
  useEffect(() => {
    resetPlayer()
  }, [source, resetPlayer])

  // 실시간 모드는 켠 뒤에 들어온 요청만 차례로 보여준다.
  const liveSteps = useRef<FlowStep[]>([])
  const liveLastID = useRef(0)
  // 지금까지 받은 가장 큰 요청 번호다. 대상을 바꿔 캐시에 남은 예전 목록이 잠깐 보여도 실시간 시작점이 뒤로 가지 않게 한다.
  // 요청 번호는 서버 전체에서 커지므로 서버 전체 목록과 대상 목록 중 큰 쪽을 쓴다.
  const seenID = useRef(0)
  const newestID = [...requestRows, ...playRows].reduce((max, row) => Math.max(max, row.id), 0)
  useEffect(() => {
    seenID.current = Math.max(seenID.current, newestID)
  }, [newestID])
  const setLiveFlow = (on: boolean) => {
    if (!on) {
      player.reset()
      return
    }
    liveSteps.current = []
    liveLastID.current = Math.max(seenID.current, newestID)
    player.startLive(liveRequestScript([], demo, scope))
  }
  useEffect(() => {
    if (!player.live || !topology.data) return
    const next = appendLiveSteps(liveSteps.current, playRows, topology.data, liveLastID.current, scope)
    liveLastID.current = next.lastID
    if (next.steps === liveSteps.current) return
    liveSteps.current = next.steps
    player.extendLive(liveRequestScript(next.steps, demo, scope), next.dropped)
    // 요청 목록이 바뀔 때만 덧붙인다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playRows, player.live])
  // 대상을 바꾸면 재생을 처음으로 돌린다. 실시간이면 켠 채로 새 대상의 요청부터 다시 모은다.
  // 같은 변경으로 재생할 내용이 API 요청에서 바뀌었으면 실시간을 다시 켜지 않는다.
  // 서버 전체를 고르는 것은 고른 것이 없는 것과 같아 다시 시작하지 않는다.
  useEffect(() => {
    if (player.live && source === 'requests') setLiveFlow(true)
    else resetPlayer()
    // 대상이 바뀔 때만 다시 시작한다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeTarget])
  useEffect(() => {
    if (!live) resetPlayer()
  }, [live, resetPlayer])

  // 선택한 것이 없으면 상세 패널을 닫아 그 폭을 구조도에 준다.
  const detailOpen = flowTab || Boolean(context.target || context.event)
  const [panelCollapsed, setPanelCollapsed] = useState(() => window.localStorage.getItem('msp.panel-collapsed') === '1')
  const togglePanel = () => {
    window.localStorage.setItem('msp.panel-collapsed', panelCollapsed ? '0' : '1')
    setPanelCollapsed(!panelCollapsed)
  }
  const playFromMap = () => {
    if (!flowTab) updateContext({ tab: 'play' })
    if (panelCollapsed) togglePanel()
    player.toggle()
  }

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
          <button type="button" className="link" onClick={() => updateContext({ from: undefined, to: undefined, at: undefined, focus: undefined })}>실시간으로 돌아가기</button>
        </div>
      )}
      <main className={`workspace ${panelCollapsed ? 'panel-collapsed' : ''} ${detailOpen ? '' : 'no-detail'}`}>
        <Explorer server={server} topology={topology.data} target={context.target} />
        <section className="map-surface" aria-label="서버와 컨테이너 구조맵">
          {servers.isError && <p className="empty">중앙 서버에서 데이터를 받지 못했습니다.</p>}
          {servers.data?.length === 0 && <p className="empty">등록된 서버가 없습니다. 아직 수신한 데이터가 없습니다.</p>}
          {topology.isError && server && <p className="empty">구조 데이터를 아직 받지 못했습니다.</p>}
          {server && topology.data && (
            <TopologyMap key={server.id} server={server} topology={topology.data} target={context.target} highlight={highlight} now={clock} live={live} selectedEvent={selectedEvent} focus={context.focus === '1'} layoutKey={`${panelCollapsed}|${detailOpen}`} player={flowTab ? player : undefined} requests={requestRows} flowPlayLabel={PLAY_LABEL[player.status]} onFlowPlay={player.script ? playFromMap : undefined} />
          )}
        </section>
        {flowTab && <FlowPanel player={player} source={source} emptyText={source === 'requests' ? requestEmpty : undefined} />}
        {detailOpen && !flowTab && <DetailPanel meta={meta.data} server={server} topology={topology.data} target={context.target} now={clock} selectedEvent={selectedEvent} events={flow.events} />}
        <BottomPanel meta={meta.data} context={context} flow={flow} selectedID={context.event} server={server} topology={topology.data} selectedEvent={selectedEvent} now={clock} collapsed={panelCollapsed} onToggleCollapsed={togglePanel} player={player} source={source} requestEmpty={requestEmpty} sources={{ requests: true, observed: Boolean(observed), simulation: Boolean(simulation) }} onSource={setSourceChoice} liveAvailable={live} onLive={setLiveFlow} />
      </main>
    </div>
  )
}
