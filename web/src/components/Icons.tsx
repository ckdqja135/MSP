import type { Status } from '../api'

const STATUS_CLASS: Record<Status, string> = {
  ok: 'ok',
  warning: 'warning',
  critical: 'critical',
  stopped: 'stopped',
  disconnected: 'disconnected',
}

// StatusIcon은 색상과 함께 모양으로도 상태를 구분한다.
export function StatusIcon({ status, size = 14 }: { status: Status; size?: number }) {
  return (
    <svg className={`status-icon ${STATUS_CLASS[status]}`} viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
      {status === 'ok' && (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path d="M4.5 8.2l2.4 2.4 4.6-5" fill="none" stroke="#fff" strokeWidth="1.8" />
        </>
      )}
      {status === 'warning' && (
        <>
          <path d="M8 1l7.2 13.5H.8z" fill="currentColor" />
          <path d="M8 5.5v4.5M8 11.4v1.4" stroke="#fff" strokeWidth="1.6" />
        </>
      )}
      {status === 'critical' && (
        <>
          <path d="M5 1h6l4 4v6l-4 4H5l-4-4V5z" fill="currentColor" />
          <path d="M5.2 5.2l5.6 5.6M10.8 5.2l-5.6 5.6" stroke="#fff" strokeWidth="1.8" />
        </>
      )}
      {status === 'stopped' && (
        <>
          <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
          <rect x="5.2" y="5.2" width="5.6" height="5.6" fill="currentColor" />
        </>
      )}
      {status === 'disconnected' && (
        <>
          <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="2.5 2" />
          <path d="M3 13L13 3" stroke="currentColor" strokeWidth="1.6" />
        </>
      )}
    </svg>
  )
}

export function UnsupportedIcon() {
  return (
    <svg className="status-icon stopped" viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M3.5 12.5l9-9" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  )
}

// 아이소메트릭 아이콘은 2D SVG로 그린다. 입체감은 면의 밝기 차이로만 표현한다.
export function IsoContainer({ stopped, size = 30 }: { stopped?: boolean; size?: number }) {
  const dash = stopped ? '2 2' : undefined
  return (
    <svg className={`iso ${stopped ? 'iso-stopped' : ''}`} viewBox="0 0 32 32" width={size} height={size} aria-hidden="true">
      <path className="iso-top" d="M16 3l12 6-12 6-12-6z" strokeDasharray={dash} />
      <path className="iso-left" d="M4 9l12 6v14L4 23z" strokeDasharray={dash} />
      <path className="iso-right" d="M28 9L16 15v14l12-6z" strokeDasharray={dash} />
      {!stopped && <path className="iso-mark" d="M7 13.5l6 3M7 17l6 3" />}
    </svg>
  )
}

export function IsoServer({ size = 52 }: { size?: number }) {
  return (
    <svg className="iso" viewBox="0 0 48 48" width={size} height={size} aria-hidden="true">
      <path className="iso-top" d="M24 3l18 9-18 9-18-9z" />
      <path className="iso-left" d="M6 12l18 9v22L6 34z" />
      <path className="iso-right" d="M42 12L24 21v22l18-9z" />
      <path className="iso-mark" d="M9 18l12 6M9 23l12 6M9 28l12 6" />
      <path className="iso-mark" d="M28 26l3-1.5M28 31l3-1.5M28 36l3-1.5" />
    </svg>
  )
}

export function IsoNetwork({ size = 16 }: { size?: number }) {
  return (
    <svg className="iso" viewBox="0 0 20 20" width={size} height={size} aria-hidden="true">
      <path className="iso-top" d="M10 2l8 4-8 4-8-4z" />
      <path className="iso-mark" d="M2 11l8 4 8-4M2 15l8 4 8-4" />
    </svg>
  )
}

export function Logo() {
  return (
    <svg className="iso" viewBox="0 0 32 32" width="28" height="28" aria-hidden="true">
      <path className="iso-top" d="M16 2l13 6.5L16 15 3 8.500z" />
      <path className="iso-left" d="M3 8.500L16 15v15L3 23.500z" />
      <path className="logo-right" d="M29 8.500L16 15v15l13-6.500z" />
    </svg>
  )
}
