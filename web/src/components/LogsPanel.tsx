import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { EventRow, Server, Service, Topology } from '../api'
import { parseTarget, timeMode, timeWindow, updateContext, type ConsoleContext } from '../context'
import { formatClock, shortID } from '../format'
import { serviceLabel } from '../status'

const MAX_LINES = 5000
const ROW_HEIGHT = 20
const FOLLOW_TAIL = 300
const RANGE_TAIL = 5000

interface Line {
  id: number
  at: string
  stream: string
  text: string
}

interface LogChunk {
  lines?: { at: string; stream: string; text: string }[]
  dropped?: number
  done?: boolean
  error?: string
}

type Status = 'connecting' | 'following' | 'done' | 'disconnected' | 'error'

interface StreamState {
  lines: Line[]
  dropped: number
  status: Status
  error?: string
}

// useLogStream은 화면에 있는 동안만 연결을 유지한다. 닫으면 중앙 서버와 에이전트도 읽기를 멈춘다.
function useLogStream(url: string | undefined, follow: boolean, attempt: number): StreamState {
  const [state, setState] = useState<StreamState>({ lines: [], dropped: 0, status: 'connecting' })
  useEffect(() => {
    if (!url) return
    setState({ lines: [], dropped: 0, status: 'connecting' })
    let nextID = 0
    const stream = new EventSource(url)
    stream.onmessage = (message) => {
      const chunk = JSON.parse(message.data) as LogChunk
      setState((previous) => {
        const incoming = (chunk.lines ?? []).map((line) => ({ ...line, id: nextID++ }))
        const lines = [...previous.lines, ...incoming].slice(-MAX_LINES)
        let status: Status = follow ? 'following' : 'connecting'
        if (chunk.done) status = chunk.error ? 'error' : 'done'
        return { lines, dropped: previous.dropped + (chunk.dropped ?? 0), status, error: chunk.error }
      })
      if (chunk.done) stream.close()
    }
    // 자동 재연결은 같은 로그를 처음부터 다시 받으므로 직접 다시 연결하게 한다.
    stream.onerror = () => {
      stream.close()
      setState((previous) => (previous.status === 'done' || previous.status === 'error' ? previous : { ...previous, status: 'disconnected' }))
    }
    return () => stream.close()
  }, [url, follow, attempt])
  return state
}

function nearestLine(lines: Line[], at: number): number {
  let best = -1
  let bestDistance = Infinity
  lines.forEach((line, index) => {
    const distance = Math.abs(Date.parse(line.at) - at)
    if (distance < bestDistance) {
      best = index
      bestDistance = distance
    }
  })
  return best
}

const STATUS_TEXT: Record<Status, string> = {
  connecting: '불러오는 중',
  following: '실시간 조회 중',
  done: '조회 완료',
  disconnected: '연결 끊김',
  error: '조회 실패',
}

function LogView({ server, service, context, selectedEvent }: { server: Server; service: Service; context: ConsoleContext; selectedEvent?: EventRow }) {
  const mode = timeMode(context)
  const follow = mode === 'live'
  const [query, setQuery] = useState('')
  const [appliedQuery, setAppliedQuery] = useState('')
  const [containerID, setContainerID] = useState('')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const timer = setTimeout(() => setAppliedQuery(query.trim()), 400)
    return () => clearTimeout(timer)
  }, [query])

  const params = new URLSearchParams({ service: service.key, follow: String(follow) })
  params.set('tail', String(follow ? FOLLOW_TAIL : RANGE_TAIL))
  if (containerID) params.set('container', containerID)
  if (appliedQuery) params.set('q', appliedQuery)
  if (!follow) {
    const { from, to } = timeWindow(context, Date.now())
    params.set('since', from)
    params.set('until', to)
  }
  const url = `/api/v1/servers/${encodeURIComponent(server.id)}/logs?${params}`
  const { lines, dropped, status, error } = useLogStream(url, follow, attempt)

  const scroller = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 20,
  })

  const eventAt = selectedEvent ? Date.parse(selectedEvent.occurred_at) : undefined
  const focusIndex = eventAt !== undefined ? nearestLine(lines, eventAt) : -1
  useLayoutEffect(() => {
    if (focusIndex >= 0) {
      virtualizer.scrollToIndex(focusIndex, { align: 'center' })
    } else if (stick.current && lines.length > 0) {
      virtualizer.scrollToIndex(lines.length - 1, { align: 'end' })
    }
    // 줄이 늘어날 때와 초점이 바뀔 때만 움직인다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lines.length, focusIndex])

  return (
    <div className="logs">
      <div className="log-tools">
        <input className="search" type="search" placeholder="키워드 (Docker에 남은 로그에서 찾음)" value={query} onChange={(e) => setQuery(e.target.value)} />
        {service.containers.length > 1 && (
          <select value={containerID} onChange={(e) => setContainerID(e.target.value)} aria-label="컨테이너 선택">
            <option value="">첫 컨테이너</option>
            {service.containers.map((container) => (
              <option key={container.id} value={container.id}>{container.name} ({shortID(container.id)})</option>
            ))}
          </select>
        )}
        <span className={`log-status ${status}`}>{STATUS_TEXT[status]}</span>
        {(status === 'disconnected' || status === 'error') && (
          <button type="button" className="tool-button" onClick={() => setAttempt(attempt + 1)}>다시 연결</button>
        )}
        <span className="muted mono">{lines.length.toLocaleString('en-US')}줄</span>
        {dropped > 0 && <span className="collect-failed">전송 한도 초과로 {dropped.toLocaleString('en-US')}줄 누락</span>}
        <span className="omitted log-note">민감정보로 보이는 값은 가려서 표시합니다. 패턴 기반이라 전부 잡지는 못합니다.</span>
      </div>
      {error && <p className="log-error">{error}</p>}
      <div
        className="log-body"
        ref={scroller}
        onScroll={(event) => {
          const element = event.currentTarget
          stick.current = element.scrollHeight - element.scrollTop - element.clientHeight < ROW_HEIGHT * 2
        }}
      >
        {!error && status !== 'connecting' && lines.length === 0 && (
          <p className="placeholder">{appliedQuery ? '검색어와 일치하는 로그가 없습니다.' : '이 범위에 로그가 없습니다. 수집되지 않은 것인지 실제로 없는 것인지는 구분되지 않습니다.'}</p>
        )}
        <div className="flow-sizer" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const line = lines[item.index]
            return (
              <div
                key={line.id}
                className={`log-line ${line.stream} ${item.index === focusIndex ? 'focused' : ''}`}
                style={{ transform: `translateY(${item.start}px)` }}
              >
                <span className="muted">{line.at && !line.at.startsWith('0001') ? formatClock(line.at) : '시각 없음'}</span>
                <span className="log-stream">{line.stream}</span>
                <span className="log-text">{line.text}</span>
              </div>
            )
          })}
        </div>
      </div>
      <p className="log-foot muted">
        {serviceLabel(service.key)}: Docker에 남아 있는 로그만 조회합니다. 저장 로그가 연동되지 않아 재생성 전 컨테이너의 로그는 볼 수 없습니다.
        {eventAt !== undefined && ' 선택한 기록과 가장 가까운 줄을 표시했습니다. 시간이 가깝다는 뜻이며 원인을 뜻하지 않습니다.'}
      </p>
    </div>
  )
}

interface Props {
  server: Server
  topology?: Topology
  context: ConsoleContext
  selectedEvent?: EventRow
}

export function LogsPanel({ server, topology, context, selectedEvent }: Props) {
  const parsed = parseTarget(context.target)
  const services = topology?.projects.flatMap((project) => project.services) ?? []
  const service = parsed?.kind === 'service' ? services.find((item) => item.key === parsed.key) : undefined
  if (!service) {
    return (
      <div className="placeholder">
        <p>로그를 볼 서비스를 선택하세요.</p>
        <div className="detail-links">
          {services.filter((item) => item.runtime !== 'stopped').slice(0, 8).map((item) => (
            <button key={item.key} type="button" className="tool-button mono" onClick={() => updateContext({ target: `svc:${item.key}` })}>
              {serviceLabel(item.key)}
            </button>
          ))}
        </div>
      </div>
    )
  }
  return <LogView key={service.key} server={server} service={service} context={context} selectedEvent={selectedEvent} />
}
