import { useEffect, useRef } from 'react'
import { PACKET_LABEL, type FlowPacket, type FlowSource, type PacketKind } from '../flow'
import { formatClock } from '../format'
import type { FlowPlayer } from '../useFlowPlayer'

const LEGEND: Record<FlowSource, PacketKind[]> = {
  simulation: ['connect', 'request', 'response'],
  observed: ['probe', 'response', 'fail'],
}

export const PLAY_LABEL = { idle: '▶ 재생', playing: '❚❚ 일시정지', paused: '▶ 계속', done: '↻ 다시 재생' } as const

interface ControlProps {
  player: FlowPlayer
  source: FlowSource
  sources: Record<FlowSource, boolean>
  onSource: (source: FlowSource) => void
  demo: boolean
}

export function FlowControls({ player, source, sources, onSource, demo }: ControlProps) {
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
        <button type="button" className={source === 'observed' ? 'active' : ''} disabled={!sources.observed} title={!sources.observed ? '응답 확인을 설정하면 사용할 수 있습니다' : demo ? '데모용 가짜 기록입니다' : '실제로 보낸 응답 확인 요청의 마지막 결과입니다'} onClick={() => onSource('observed')}>
          {demo ? '데모 기록' : '실제 기록'}
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
        <button type="button" className="tool-button" title="재생 속도" onClick={player.cycleSpeed}>{player.speed}×</button>
        <button type="button" className="tool-button" title="이전 단계 (←)" disabled={player.status === 'idle'} onClick={player.previous}>‹</button>
        <button type="button" className="tool-button play-button" title="재생과 일시정지 (Space)" disabled={!player.script} onClick={player.toggle}>{PLAY_LABEL[player.status]}</button>
        <button type="button" className="tool-button" title="패킷 하나씩" disabled={!player.script} onClick={player.step}>한 칸</button>
        <button type="button" className="tool-button" title="개요로 돌아가기 (Esc)" disabled={player.status === 'idle'} onClick={player.reset}>처음으로</button>
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
      <span>{packet.result}</span>
    </button>
  )
}

export function FlowPacketTable({ player, source }: { player: FlowPlayer; source: FlowSource }) {
  const packets = player.timeline?.packets ?? []
  const frame = player.frame
  const shown = packets.slice(0, frame?.delivered ?? packets.length)
  const currentRow = player.status === 'idle' || !frame ? -1 : frame.delivered - 1
  if (!player.script) {
    return (
      <p className="placeholder">
        {source === 'simulation' ? '외부로 공개된 포트가 없어 그릴 경로가 없습니다.' : '재생할 응답 확인 기록이 없습니다.'}
      </p>
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
        <span role="columnheader">결과</span>
      </div>
      <div className="packet-rows">
        {shown.length === 0 && <p className="placeholder">아직 도착한 패킷이 없습니다.</p>}
        {shown.map((packet, index) => (
          <PacketRow key={packet.id} packet={packet} number={index + 1} current={index === currentRow} onSeek={() => player.seekPacket(index)} />
        ))}
      </div>
    </div>
  )
}

export function FlowPanel({ player }: { player: FlowPlayer }) {
  const { script, timeline, frame, status } = player
  if (!script || !timeline || !frame) {
    return (
      <aside className="detail flow-panel" aria-label="통신 재생 설명">
        <h2 className="panel-title">통신 재생</h2>
        <p className="muted empty">재생할 내용이 없습니다.</p>
      </aside>
    )
  }
  const done = status === 'done'
  const overview = status === 'idle'
  const step = !overview && !done ? script.steps[frame.step] : undefined
  const packet = !overview && !done && frame.phase !== 'intro' ? timeline.packets[frame.packet] : undefined
  const total = timeline.packets.length
  return (
    <aside className="detail flow-panel" aria-label="통신 재생 설명">
      <h2 className="panel-title">{overview ? '통신 재생' : done ? '재생 완료' : `단계 ${frame.step + 1} / ${script.steps.length}`}</h2>
      <section className="detail-section">
        <h3>{step ? step.title : script.title}</h3>
        {step && <p className="muted small">{step.description}</p>}
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
      <ol className="flow-step-list">
        {script.steps.map((item, index) => {
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
