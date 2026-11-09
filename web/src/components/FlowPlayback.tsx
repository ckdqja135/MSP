import { useEffect, useRef } from 'react'
import { PACKET_LABEL, type FlowPacket, type FlowSource, type PacketKind } from '../flow'
import { formatBytes, formatClock } from '../format'
import type { FlowPlayer } from '../useFlowPlayer'

const LEGEND: Record<FlowSource, PacketKind[]> = {
  requests: ['request', 'response', 'fail'],
  simulation: ['connect', 'request', 'response'],
  observed: ['probe', 'response', 'fail'],
}

export const PLAY_LABEL = { idle: '▶ 재생', playing: '❚❚ 일시정지', paused: '▶ 계속', done: '↻ 다시 재생', waiting: '❚❚ 일시정지' } as const

interface ControlProps {
  player: FlowPlayer
  source: FlowSource
  sources: Record<FlowSource, boolean>
  onSource: (source: FlowSource) => void
  demo: boolean
  // 화면 고정이나 과거 구간에서는 새 요청이 들어오지 않으므로 실시간을 켤 수 없다.
  liveAvailable: boolean
  onLive: (on: boolean) => void
}

export function FlowControls({ player, source, sources, onSource, demo, liveAvailable, onLive }: ControlProps) {
  const { toggle, previous, reset } = player
  // 스페이스, 왼쪽 화살표, Esc로 조작한다. 입력 중이거나 버튼에 초점이 있으면 그쪽 동작을 따른다.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const element = event.target as HTMLElement
      if (element.closest('input, select, textarea, button')) return
      if (event.code === 'Space') {
        event.preventDefault()
        toggle()
      } else if (event.code === 'ArrowLeft') {
        previous()
      } else if (event.code === 'Escape') {
        reset()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle, previous, reset])

  return (
    <div className="flow-controls">
      <div className="flow-sources" role="group" aria-label="재생할 내용">
        <button type="button" className={source === 'requests' ? 'active' : ''} disabled={!sources.requests} title={!sources.requests ? '에이전트의 access_logs를 설정하면 접근 로그의 실제 요청을 볼 수 있습니다' : demo ? '데모용 가짜 요청입니다' : '접근 로그에서 읽은 실제 요청입니다'} onClick={() => onSource('requests')}>
          API 요청{demo ? ' (데모)' : ''}
        </button>
        <button type="button" className={source === 'observed' ? 'active' : ''} disabled={!sources.observed} title={!sources.observed ? '응답 확인을 설정하면 사용할 수 있습니다' : demo ? '데모용 가짜 기록입니다' : '실제로 보낸 응답 확인 요청의 마지막 결과입니다'} onClick={() => onSource('observed')}>
          응답 확인{demo ? ' (데모)' : ''}
        </button>
        <button type="button" className={source === 'simulation' ? 'active' : ''} disabled={!sources.simulation} title="구성을 따라 그린 예시입니다. 실제 통신이 아닙니다" onClick={() => onSource('simulation')}>
          시뮬레이션
        </button>
      </div>
      <div className="flow-legend">
        {LEGEND[source].map((kind) => (
          <span key={kind} className={`packet-${kind}`}>
            <i />
            {PACKET_LABEL[kind]}
          </span>
        ))}
      </div>
      <div className="flow-actions">
        <button
          type="button"
          className={`tool-button live-button ${player.live ? 'active' : ''}`}
          aria-pressed={player.live}
          disabled={source !== 'requests' || !liveAvailable}
          title={source !== 'requests' ? 'API 요청에서만 쓸 수 있습니다' : !liveAvailable ? '실시간 조회일 때만 쓸 수 있습니다' : '새 요청이 들어오는 대로 보여줍니다'}
          onClick={() => onLive(!player.live)}
        >
          {player.live ? '● 실시간 끄기' : '● 실시간'}
        </button>
        <button type="button" className="tool-button" title="재생 속도" onClick={player.cycleSpeed}>{player.speed}×</button>
        <button type="button" className="tool-button" title="이전 단계 (←)" disabled={player.status === 'idle' || player.live} onClick={player.previous}>‹</button>
        <button type="button" className="tool-button play-button" title="재생과 일시정지 (Space)" disabled={!player.script} onClick={player.toggle}>{PLAY_LABEL[player.status]}</button>
        <button type="button" className="tool-button" title="패킷 하나씩" disabled={!player.script || player.live} onClick={player.step}>한 칸</button>
        <button type="button" className="tool-button" title="개요로 돌아가기 (Esc)" disabled={player.status === 'idle'} onClick={() => (player.live ? onLive(false) : player.reset())}>처음으로</button>
      </div>
    </div>
  )
}

function PacketRow({ packet, number, current, onSeek }: { packet: FlowPacket; number: number; current: boolean; onSeek: () => void }) {
  const row = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (current) row.current?.scrollIntoView({ block: 'nearest' })
  }, [current])
  return (
    <button ref={row} type="button" role="row" className={`packet-row ${current ? 'current' : ''}`} aria-current={current} onClick={onSeek}>
      <span className={`packet-${packet.kind}`}><span className="packet-number">{number}</span></span>
      <span className="mono">{packet.at ? formatClock(packet.at) : '—'}</span>
      <span className="ellipsis">{packet.from}</span>
      <span className="ellipsis">{packet.to}</span>
      <span className="mono">{packet.protocol}</span>
      <span className="ellipsis" title={`${packet.title} ${packet.detail}`}>{packet.title} · {packet.detail}</span>
      <span className="mono">{packet.durationMS !== null ? `${packet.durationMS}ms` : '—'}</span>
      <span className="mono">{packet.bytes !== null ? formatBytes(packet.bytes) : '—'}</span>
      <span>{packet.result}</span>
    </button>
  )
}

// emptyText는 대상을 골랐는데 그 대상에 닿은 요청이 없을 때의 안내다.
export function FlowPacketTable({ player, source, emptyText }: { player: FlowPlayer; source: FlowSource; emptyText?: string }) {
  const packets = player.timeline?.packets ?? []
  const frame = player.frame
  const shown = packets.slice(0, frame?.delivered ?? packets.length)
  const currentRow = player.status === 'idle' || !frame ? -1 : frame.delivered - 1
  if (!player.script) {
    return (
      <div className="placeholder">
        {source === 'simulation' && <p>외부로 공개된 포트가 없어 그릴 경로가 없습니다.</p>}
        {source === 'observed' && <p>재생할 응답 확인 기록이 없습니다.</p>}
        {source === 'requests' && emptyText && (
          <>
            <p>{emptyText}</p>
            <p className="muted small">● 실시간을 켜 두면 새 요청이 들어오는 대로 보여줍니다.</p>
          </>
        )}
        {source === 'requests' && !emptyText && (
          <>
            <p>이 구간에 접근 로그에서 읽은 실제 요청이 없습니다. 예시를 대신 보여주지 않습니다.</p>
            <p className="muted small">접속이 없었거나, nginx가 다른 파일에 로그를 쓰거나, 로그 형식이 달라 읽지 못했을 수 있습니다. ● 실시간을 켜 두면 새 요청이 들어오는 대로 보여줍니다.</p>
          </>
        )}
      </div>
    )
  }
  return (
    <div className="packet-table" role="table" aria-label="패킷 흐름">
      <div className="packet-row packet-head" role="row">
        <span role="columnheader">번호</span>
        <span role="columnheader">시간</span>
        <span role="columnheader">출발</span>
        <span role="columnheader">도착</span>
        <span role="columnheader">프로토콜</span>
        <span role="columnheader">내용</span>
        <span role="columnheader">소요</span>
        <span role="columnheader">크기</span>
        <span role="columnheader">결과</span>
      </div>
      <div className="packet-rows">
        {shown.length === 0 && <p className="placeholder">{player.live ? '새 요청을 기다리는 중입니다. 들어오는 대로 보여줍니다.' : '아직 도착한 패킷이 없습니다.'}</p>}
        {shown.map((packet, index) => (
          <PacketRow key={packet.id} packet={packet} number={index + 1} current={index === currentRow} onSeek={() => player.seekPacket(index)} />
        ))}
      </div>
    </div>
  )
}

const EMPTY_TEXT: Record<FlowSource, string> = {
  requests: '이 구간에 읽은 실제 API 요청이 없습니다. 예시를 대신 보여주지 않습니다. ● 실시간을 켜 두면 새 요청이 들어오는 대로 보여줍니다.',
  observed: '재생할 응답 확인 기록이 없습니다.',
  simulation: '외부로 공개된 포트가 없어 그릴 경로가 없습니다.',
}

// 실시간 모드에서는 단계 목록이 길어지므로 최근 것만 보여준다.
const LIVE_LIST = 8

export function FlowPanel({ player, source, emptyText }: { player: FlowPlayer; source: FlowSource; emptyText?: string }) {
  const { script, timeline, frame, status } = player
  if (!script || !timeline || !frame) {
    return (
      <aside className="detail flow-panel" aria-label="통신 재생 설명">
        <h2 className="panel-title">통신 재생</h2>
        <p className="muted empty">{emptyText ?? EMPTY_TEXT[source]}</p>
      </aside>
    )
  }
  const waiting = status === 'waiting'
  const done = status === 'done' || waiting
  const overview = status === 'idle'
  const step = !overview && !done ? script.steps[frame.step] : undefined
  const packet = !overview && !done && frame.phase !== 'intro' ? timeline.packets[frame.packet] : undefined
  const total = timeline.packets.length
  const first = player.live ? Math.max(0, script.steps.length - LIVE_LIST) : 0
  let title = overview ? '통신 재생' : done ? '재생 완료' : `단계 ${frame.step + 1} / ${script.steps.length}`
  if (player.live) title = waiting ? '실시간 · 새 요청 대기' : status === 'paused' ? '실시간 · 일시정지' : '실시간'
  return (
    <aside className="detail flow-panel" aria-label="통신 재생 설명">
      <h2 className="panel-title">{title}</h2>
      <section className="detail-section">
        <h3>{step ? step.title : script.title}</h3>
        {step && <p className="muted small">{step.description}</p>}
        {waiting && <p className="muted small">새 요청이 들어오면 바로 보여줍니다.</p>}
        {player.skipped > 0 && <p className="muted small">요청이 몰려 {player.skipped}건은 건너뛰었습니다. 표에는 최근 것만 남습니다.</p>}
      </section>
      {packet && (
        <div className={`flow-packet-card packet-${packet.kind}`} aria-live="polite">
          <header>
            <span>{packet.title}</span>
            <span>{PACKET_LABEL[packet.kind]}</span>
          </header>
          <div>{packet.from} → {packet.to}</div>
          <div>{packet.detail}</div>
          <div className="muted small">패킷 {frame.packet + 1} / {total} · {packet.protocol}{packet.durationMS !== null ? ` · ${packet.durationMS}ms` : ''}</div>
        </div>
      )}
      <ol className="flow-step-list" start={first + 1}>
        {script.steps.slice(first).map((item, offset) => {
          const index = first + offset
          const state = done || index < frame.step ? 'passed' : !overview && index === frame.step ? 'current' : ''
          return (
            <li key={item.id} className={state}>
              <span className="flow-step-mark">{state === 'passed' ? '✓' : index + 1}</span>
              <span>
                <div className="flow-step-title">{item.title}</div>
                <div className="flow-step-summary">{item.summary}</div>
              </span>
            </li>
          )
        })}
      </ol>
      <p className={`flow-note ${script.source}`}>{script.note}</p>
      <section className="detail-section">
        {script.facts.map(([label, value]) => (
          <div key={label} className="detail-row">
            <span className="detail-label">{label}</span>
            <span className="detail-value">{value}</span>
          </div>
        ))}
      </section>
    </aside>
  )
}
