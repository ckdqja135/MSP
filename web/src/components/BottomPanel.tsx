import { useLayoutEffect, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { EventRow, GapRow, Meta } from '../api'
import { parseTarget, updateContext, type ConsoleContext } from '../context'
import { formatClock, formatDuration, formatSize, isLateArrival } from '../format'
import { CATEGORY_LABEL, evidenceLabel, serviceLabel, sourceLabel, typeLabel } from '../status'
import type { FlowData, FlowRecord } from '../useFlowRecords'
import { StatusIcon } from './Icons'

const ROW_HEIGHT = 26

const severityStatus = { info: 'ok', warning: 'warning', critical: 'critical' } as const

function EventCells({ event }: { event: EventRow }) {
  return (
    <>
      <span className="flow-time mono">
        {formatClock(event.occurred_at)}
        {isLateArrival(event) && <span className="late-tag" title={`수신 ${formatClock(event.received_at)}`}>지연</span>}
      </span>
      <span className="ellipsis">{sourceLabel(event.source)}</span>
      <span className="mono ellipsis" title={event.target}>{event.target || '—'}</span>
      <span className="flow-type">
        <StatusIcon status={severityStatus[event.severity]} size={12} />
        <span className="ellipsis">{typeLabel(event.type)}</span>
      </span>
      <span className="ellipsis" title={event.summary}>
        {event.summary}
        {event.aggregated > 1 && <span className="muted"> ({event.aggregated}건 집계)</span>}
      </span>
      <span className="mono ellipsis">{formatDuration(event)}</span>
      <span className="mono">{formatSize(event)}</span>
      <span className="ellipsis">{event.result || '—'}</span>
      <span className="ellipsis muted">{evidenceLabel(event.evidence)}</span>
    </>
  )
}

function GapCells({ gap }: { gap: GapRow }) {
  return (
    <>
      <span className="flow-time mono" title={`${formatClock(gap.from)} ~ ${formatClock(gap.to)}`}>{formatClock(gap.to)}</span>
      <span>—</span>
      <span>—</span>
      <span className="flow-type">데이터 공백</span>
      <span className="ellipsis">
        {formatClock(gap.from)} ~ {formatClock(gap.to)} 수집 없음: {gap.reason}
        {gap.dropped > 0 && ` (${gap.dropped}건 누락)`}
      </span>
      <span>—</span>
      <span>—</span>
      <span>수집 없음</span>
      <span className="muted">수신 기록</span>
    </>
  )
}

function FlowTable({ flow, selectedID }: { flow: FlowData; selectedID?: string }) {
  const scroller = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: flow.records.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })

  // 위로 새 행이 들어와도 보고 있던 행이 밀리지 않게 스크롤 위치를 보정한다.
  const firstKey = useRef<string>('')
  useLayoutEffect(() => {
    const element = scroller.current
    const key = (record: FlowRecord) => (record.kind === 'event' ? `e${record.event.id}` : `g${record.gap.to}`)
    const current = flow.records[0] ? key(flow.records[0]) : ''
    if (element && firstKey.current && current !== firstKey.current && element.scrollTop > 0) {
      const added = flow.records.findIndex((record) => key(record) === firstKey.current)
      if (added > 0) element.scrollTop += added * ROW_HEIGHT
    }
    firstKey.current = current
  }, [flow.records])

  return (
    <div className="flow-table" role="table" aria-label="이벤트 흐름표">
      <div className="flow-row flow-header" role="row">
        <span role="columnheader">시간</span>
        <span role="columnheader">출발</span>
        <span role="columnheader">도착</span>
        <span role="columnheader">유형</span>
        <span role="columnheader">내용</span>
        <span role="columnheader">소요</span>
        <span role="columnheader">크기</span>
        <span role="columnheader">결과</span>
        <span role="columnheader">근거</span>
      </div>
      <div className="flow-body" ref={scroller}>
        {flow.isError && <p className="placeholder">기록을 불러오지 못했습니다.</p>}
        {!flow.isError && !flow.isLoading && flow.records.length === 0 && (
          <p className="placeholder">이 조건에 맞는 기록이 없습니다. 수집되지 않은 항목은 표 위의 안내를 확인하세요.</p>
        )}
        <div className="flow-sizer" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const record = flow.records[item.index]
            const style = { transform: `translateY(${item.start}px)` }
            if (record.kind === 'gap') {
              return (
                <div key={`gap-${record.gap.to}`} role="row" className="flow-row flow-item gap-row" style={style}>
                  <GapCells gap={record.gap} />
                </div>
              )
            }
            const selected = String(record.event.id) === selectedID
            return (
              <button
                key={record.event.id}
                type="button"
                role="row"
                aria-selected={selected}
                className={`flow-row flow-item ${selected ? 'selected' : ''}`}
                style={style}
                onClick={() => updateContext({ event: selected ? undefined : String(record.event.id) })}
              >
                <EventCells event={record.event} />
              </button>
            )
          })}
        </div>
      </div>
      {(flow.hasMore || flow.records.length > 0) && (
        <div className="flow-more">
          <span>{flow.events.length.toLocaleString('en-US')}건 표시</span>
          {flow.hasMore && (
            <button type="button" className="link" disabled={flow.loadingMore} onClick={flow.loadMore}>이전 기록 더 보기</button>
          )}
        </div>
      )}
    </div>
  )
}

function targetLabel(target?: string): string | undefined {
  const parsed = parseTarget(target)
  if (!parsed || parsed.kind === 'host') return undefined
  if (parsed.kind === 'service') return serviceLabel(parsed.key)
  if (parsed.kind === 'network') return `네트워크 ${parsed.name}`
  return `프로젝트 ${parsed.key === '_standalone' ? '미분류' : parsed.key}`
}

interface Props {
  meta?: Meta
  context: ConsoleContext
  flow: FlowData
  selectedID?: string
}

export function BottomPanel({ meta, context, flow, selectedID }: Props) {
  const selectedCategories = context.categories ? context.categories.split(',') : Object.keys(CATEGORY_LABEL)
  const toggleCategory = (category: string) => {
    const next = selectedCategories.includes(category)
      ? selectedCategories.filter((item) => item !== category)
      : [...selectedCategories, category]
    const all = next.length === Object.keys(CATEGORY_LABEL).length
    updateContext({ categories: all ? undefined : next.join(',') || 'none' })
  }
  const label = targetLabel(context.target)
  const notes: string[] = []
  if (meta?.integrations.request_tracing === 'not_connected') notes.push('요청 추적 미연동')
  if (meta?.integrations.packet_capture === 'not_connected') notes.push('패킷 미수집')

  return (
    <section className="bottom-panel" aria-label="기록">
      <div className="bottom-head">
        <div className="tabs" role="tablist">
          {[
            ['flow', '이벤트 흐름표'],
            ['metrics', '지표'],
            ['logs', '로그'],
          ].map(([id, text]) => (
            <button key={id} type="button" role="tab" aria-selected={context.tab === id} className={context.tab === id ? 'active' : ''} onClick={() => updateContext({ tab: id })}>
              {text}
            </button>
          ))}
        </div>
        {context.tab === 'flow' && (
          <>
            <span className="omitted">
              표시되지 않는 것: {notes.join(', ')}
              {flow.gaps.length > 0 && <> · <b>수집 공백 {flow.gaps.length}구간</b></>}
            </span>
            <div className="filters">
              {label && (
                <button type="button" className="target-chip" title="대상 필터 해제" onClick={() => updateContext({ target: undefined, event: undefined })}>
                  대상: {label} ✕
                </button>
              )}
              {Object.entries(CATEGORY_LABEL).map(([category, text]) => (
                <label key={category}>
                  <input type="checkbox" checked={selectedCategories.includes(category)} onChange={() => toggleCategory(category)} />
                  {text}
                </label>
              ))}
              <label>
                <input type="checkbox" checked={context.errors === 'true'} onChange={(e) => updateContext({ errors: e.target.checked ? 'true' : undefined })} />
                오류만
              </label>
            </div>
          </>
        )}
      </div>
      {context.tab === 'flow' && <FlowTable flow={flow} selectedID={selectedID} />}
      {context.tab === 'metrics' && <p className="placeholder">지표 차트는 다음 단계에서 추가합니다.</p>}
      {context.tab === 'logs' && <p className="placeholder">로그 조회는 다음 단계에서 추가합니다.</p>}
    </section>
  )
}
