import type { Meta, Server, Topology } from '../api'
import { AlertsMenu } from './AlertsMenu'
import { RANGES, timeMode, updateContext, type ConsoleContext } from '../context'
import { formatAge, formatDateTime } from '../format'
import { Logo, StatusIcon } from './Icons'

const RANGE_LABEL: Record<string, string> = { '15m': '최근 15분', '1h': '최근 1시간', '6h': '최근 6시간', '24h': '최근 24시간' }

interface Props {
  meta?: Meta
  servers: Server[]
  server?: Server
  topology?: Topology
  context: ConsoleContext
  now: number
  streamConnected: boolean
}

export function TopBar({ meta, servers, server, topology, context, now, streamConnected }: Props) {
  const counts = { critical: 0, warning: 0 }
  for (const project of topology?.projects ?? []) {
    for (const service of project.services) {
      if (service.status === 'critical') counts.critical++
      if (service.status === 'warning') counts.warning++
    }
  }
  const mode = timeMode(context)
  const reachable = server?.reachability.state ?? 'unknown'

  return (
    <header className="topbar">
      <div className="brand">
        <Logo />
        <div>
          <strong>MSP</strong>
          <span className="muted">서버 콘솔</span>
        </div>
      </div>

      {meta?.mode === 'demo' && <span className="demo-badge" title="가짜 데이터입니다. 실제 서버와 연결되어 있지 않습니다">DEMO 가짜 데이터</span>}

      <label className="field">
        <span className="field-label">서버</span>
        <select value={server?.id ?? ''} onChange={(e) => updateContext({ server: e.target.value, target: undefined, event: undefined })}>
          {servers.map((item) => (
            <option key={item.id} value={item.id}>{item.id}</option>
          ))}
        </select>
      </label>

      <div className="field">
        <span className="field-label">상태</span>
        <div className="summary">
          {reachable !== 'ok' && server && (
            <span className="badge disconnected" title={server.reachability.detail}>
              <StatusIcon status="disconnected" size={12} />
              {server.reachability.label}
            </span>
          )}
          {counts.critical > 0 && (
            <span className="badge critical"><StatusIcon status="critical" size={12} />장애 {counts.critical}</span>
          )}
          {counts.warning > 0 && (
            <span className="badge warning"><StatusIcon status="warning" size={12} />주의 {counts.warning}</span>
          )}
          {reachable === 'ok' && counts.critical + counts.warning === 0 && (
            <span className="badge ok"><StatusIcon status="ok" size={12} />정상</span>
          )}
        </div>
      </div>

      <div className="field">
        <span className="field-label">마지막 수신</span>
        <span className="mono">{server?.last_seen ? formatAge(server.last_seen, now) : '기록 없음'}</span>
      </div>

      <div className="field">
        <span className="field-label">에이전트</span>
        <span className="mono">{server?.agent_version || '알 수 없음'}</span>
      </div>

      <div className="spacer" />

      <AlertsMenu demo={meta?.mode === 'demo'} />

      {!streamConnected && <span className="badge warning" title="화면에는 마지막으로 받은 값이 표시됩니다">중앙 서버 연결 끊김</span>}

      <label className="field">
        <span className="field-label">조회 범위</span>
        <select
          value={context.range}
          disabled={mode === 'past'}
          onChange={(e) => updateContext({ range: e.target.value })}
        >
          {Object.keys(RANGES).map((key) => (
            <option key={key} value={key}>{RANGE_LABEL[key]}</option>
          ))}
        </select>
      </label>

      <div className="time-mode" role="group" aria-label="화면 갱신">
        <button
          type="button"
          className={mode === 'live' ? 'active' : ''}
          onClick={() => updateContext({ frozen: undefined, from: undefined, to: undefined, at: undefined, focus: undefined })}
        >
          <i className="live-dot" />실시간
        </button>
        <button
          type="button"
          className={mode === 'frozen' ? 'active' : ''}
          disabled={mode === 'past'}
          title="화면 갱신만 멈춥니다. 서버와 데이터 수집에는 영향을 주지 않습니다"
          onClick={() => updateContext({ frozen: new Date(now).toISOString() })}
        >
          화면 고정
        </button>
      </div>

      {mode === 'past' && context.from && context.to && (
        <span className="badge past">과거 구간 {formatDateTime(context.from)} ~ {formatDateTime(context.to)}</span>
      )}
    </header>
  )
}
