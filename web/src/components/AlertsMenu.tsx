import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchAlerts, type Alert, type Status } from '../api'
import { updateContext } from '../context'
import { formatDateTime } from '../format'
import { StatusIcon } from './Icons'

const AROUND_MS = 15 * 60_000

const STATE_LABEL: Record<Alert['state'], string> = {
  firing: '진행 중',
  flapping: '반복 발생',
  resolved: '복구됨',
}

function alertStatus(alert: Alert): Status {
  if (alert.state === 'resolved') return 'ok'
  return alert.severity === 'critical' ? 'critical' : 'warning'
}

// openAlert는 장애 시간대와 대상이 선택된 화면을 연다. 알림 링크와 같은 맥락이다.
function openAlert(alert: Alert) {
  const at = alert.fired_at ? Date.parse(alert.fired_at) : Date.now()
  updateContext({
    server: alert.server_id,
    target: alert.target,
    from: new Date(at - AROUND_MS).toISOString(),
    to: new Date(Math.min(at + AROUND_MS, Date.now())).toISOString(),
    at: new Date(at).toISOString(),
    tab: 'flow',
    event: undefined,
    frozen: undefined,
    focus: '1',
  })
}

export function AlertsMenu({ demo }: { demo: boolean }) {
  const [open, setOpen] = useState(false)
  const alerts = useQuery({ queryKey: ['live', 'alerts'], queryFn: fetchAlerts, refetchInterval: 15_000 })
  const list = alerts.data ?? []
  const active = list.filter((alert) => alert.state !== 'resolved')
  return (
    <div className="alerts">
      <button type="button" className={`alerts-button ${active.length > 0 ? 'active' : ''}`} aria-expanded={open} onClick={() => setOpen(!open)}>
        {active.length > 0 && <StatusIcon status="critical" size={12} />}
        알림 {active.length}
      </button>
      {open && (
        <div className="alerts-panel">
          <div className="alerts-note">
            {demo ? '데모에서는 외부로 알림을 보내지 않습니다.' : '응답 중단만으로 정전이나 OS 장애를 단정하지 않습니다.'}
          </div>
          {alerts.isError && <p className="placeholder">알림을 불러오지 못했습니다.</p>}
          {!alerts.isError && list.length === 0 && <p className="placeholder">진행 중이거나 최근 24시간 안에 복구된 알림이 없습니다.</p>}
          {list.map((alert) => (
            <button
              key={alert.id}
              type="button"
              className="alert-row"
              onClick={() => {
                setOpen(false)
                openAlert(alert)
              }}
            >
              <StatusIcon status={alertStatus(alert)} size={13} />
              <span className="alert-title">{alert.title}</span>
              <span className="alert-time">{alert.fired_at ? formatDateTime(alert.fired_at) : ''}</span>
              <span className="alert-detail">{STATE_LABEL[alert.state]}{alert.detail ? ` · ${alert.detail}` : ''}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
