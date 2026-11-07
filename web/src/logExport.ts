import type { EventRow, GapRow, SampleSeries } from './api'
import type { FlowPacket, FlowScript } from './flow'
import { PACKET_LABEL } from './flow'

// 화면에 보이는 기록을 .log 파일로 만든다. 브라우저 안에서만 만들어지며 어디로도 전송하지 않는다.

export interface LogFile {
  name: string
  text: string
  count: number
}

export interface LogHeader {
  title: string
  server: string
  demo: boolean
  range?: { from: string; to: string }
  conditions?: string[]
  notes?: string[]
}

const pad = (value: number, size = 2) => String(value).padStart(size, '0')

// logTime은 시간대가 드러나는 지역 시각으로 적는다. 파일만 봐도 언제인지 알 수 있어야 한다.
export function logTime(value: string | number): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 1000) return '-'
  const offset = -date.getTimezoneOffset()
  const sign = offset < 0 ? '-' : '+'
  const zone = `${sign}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${zone}`
}

// cleanText는 한 기록이 한 줄로 남게 하고 터미널을 조작하는 제어 문자를 글자로 바꾼다.
export function cleanText(text: string): string {
  return text
    .replace(/\r\n|\r|\n/g, '\\n')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, (char) => `\\x${char.charCodeAt(0).toString(16).padStart(2, "0")}`)
}

const quoted = (text: string) => `"${cleanText(text).replace(/"/g, '\\"')}"`
const field = (value: string | number | null | undefined) => (value === null || value === undefined || value === '' ? '-' : /[\s"=]/.test(String(value)) ? quoted(String(value)) : cleanText(String(value)))

function headerLines(header: LogHeader, count: number, now: number): string[] {
  return [
    `# MSP ${header.title}`,
    `# 서버: ${cleanText(header.server)}`,
    ...(header.range ? [`# 구간: ${logTime(header.range.from)} ~ ${logTime(header.range.to)}`] : []),
    ...(header.conditions ?? []).map((item) => `# 조건: ${cleanText(item)}`),
    `# 건수: ${count}`,
    `# 내보낸 시각: ${logTime(now)}`,
    ...(header.demo ? ['# 주의: 데모용 가짜 데이터입니다. 실제 서버의 기록이 아닙니다.'] : []),
    ...(header.notes ?? []).map((item) => `# 참고: ${cleanText(item)}`),
    '',
  ]
}

function compose(header: LogHeader, lines: string[], name: string, now: number, count = lines.length): LogFile {
  return { name, count, text: `${[...headerLines(header, count, now), ...lines].join('\n')}\n` }
}

// logFileName은 파일 이름에 쓸 수 없는 글자를 지운다. 데모 파일은 이름에서도 구분된다.
export function logFileName(server: string, parts: string[], demo: boolean, now: number): string {
  const date = new Date(now)
  const stamp = `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}-${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`
  const slug = ['msp', ...(demo ? ['demo'] : []), server, ...parts, stamp].join('-').replace(/[^\p{L}\p{N}._-]+/gu, '-')
  return `${slug}.log`
}

const LEVEL: Record<EventRow['severity'], string> = { info: 'INFO', warning: 'WARN', critical: 'CRIT' }

export function eventLine(event: EventRow): string {
  return [
    logTime(event.occurred_at),
    LEVEL[event.severity],
    `type=${field(event.type)}`,
    `source=${field(event.source)}`,
    `target=${field(event.target)}`,
    `service=${field(event.service_key)}`,
    `duration_ms=${field(event.duration_ms)}`,
    ...(event.duration_limit_ms !== null ? [`duration_over_ms=${event.duration_limit_ms}`] : []),
    `bytes=${field(event.bytes)}`,
    `result=${field(event.result)}`,
    `evidence=${field(event.evidence)}`,
    ...(event.aggregated > 1 ? [`aggregated=${event.aggregated}`] : []),
    `msg=${quoted(event.summary)}`,
  ].join(' ')
}

export function gapLine(gap: GapRow): string {
  return [logTime(gap.to), 'GAP', `from=${logTime(gap.from)}`, `source=${field(gap.source)}`, `dropped=${gap.dropped}`, `msg=${quoted(gap.reason)}`].join(' ')
}

// eventsLog는 오래된 기록부터 적는다. 수집 공백도 시간 순서대로 끼워 넣어 숨기지 않는다.
export function eventsLog(events: EventRow[], gaps: GapRow[], header: Omit<LogHeader, 'title'>, truncated: boolean, now: number): LogFile {
  const records = [
    ...events.map((event) => ({ at: Date.parse(event.occurred_at), order: event.id, line: eventLine(event) })),
    ...gaps.map((gap) => ({ at: Date.parse(gap.to), order: 0, line: gapLine(gap) })),
  ].sort((a, b) => a.at - b.at || a.order - b.order)
  return compose(
    { ...header, title: '이벤트 흐름표', notes: [...(header.notes ?? []), ...(truncated ? [`기록이 많아 최근 ${events.length}건까지만 담았습니다. 구간을 줄이면 나머지를 받을 수 있습니다.`] : [])] },
    records.map((record) => record.line),
    logFileName(header.server, ['events'], header.demo, now),
    now,
  )
}

function packetLine(packet: FlowPacket, number: number): string {
  return [
    packet.at ? logTime(packet.at) : '-',
    PACKET_LABEL[packet.kind],
    `no=${number}`,
    `from=${field(packet.from)}`,
    `to=${field(packet.to)}`,
    `protocol=${field(packet.protocol)}`,
    `duration_ms=${field(packet.durationMS)}`,
    `bytes=${field(packet.bytes)}`,
    `result=${field(packet.result)}`,
    `msg=${quoted(`${packet.title} ${packet.detail}`)}`,
  ].join(' ')
}

export function playbackLog(script: FlowScript, server: string, demo: boolean, now: number): LogFile {
  const packets = script.steps.flatMap((step) => step.packets)
  const label = { simulation: '시뮬레이션', observed: '응답 확인 기록', requests: 'API 요청' }[script.source]
  return compose(
    {
      title: `통신 재생 (${label})`,
      server,
      demo,
      conditions: [script.title],
      notes: [script.note],
    },
    packets.map((packet, index) => packetLine(packet, index + 1)),
    logFileName(server, ['playback', script.source], demo, now),
    now,
  )
}

export interface MetricSeries extends SampleSeries {
  scope: string
  stepMS: number
}

// metricsLog는 값이 있는 칸만 적는다. 값이 없는 구간을 0으로 채우면 측정값처럼 보이기 때문이다.
export function metricsLog(series: MetricSeries[], gaps: GapRow[], header: Omit<LogHeader, 'title' | 'notes'>, target: string, now: number): LogFile {
  const lines = series.flatMap((item) =>
    item.points.map((point) => `${logTime(point.t)} METRIC scope=${field(item.scope)} name=${field(item.name)} value=${point.v} step_ms=${item.stepMS}`),
  )
  const steps = [...new Set(series.map((item) => item.stepMS))]
  return compose(
    { ...header, title: '지표', notes: [`값은 약 ${steps.join(', ')}ms 칸의 평균으로 그래프와 같습니다.`, `수집 공백 ${gaps.length}구간은 값을 적지 않았습니다.`] },
    [...lines, ...gaps.map(gapLine)],
    logFileName(header.server, ['metrics', target], header.demo, now),
    now,
    lines.length,
  )
}

export interface LogLineRow {
  at: string
  stream: string
  text: string
}

export function containerLog(lines: LogLineRow[], header: Omit<LogHeader, 'title' | 'notes'>, service: string, status: string, dropped: number, now: number): LogFile {
  return compose(
    {
      ...header,
      title: `로그 ${service}`,
      notes: [
        '화면에 받아 둔 줄만 담았습니다. Docker에 남아 있는 로그 전체가 아닐 수 있습니다.',
        '민감정보로 보이는 값은 가렸지만 패턴 기반이라 전부 잡지는 못합니다. 공유하기 전에 확인하세요.',
        `조회 상태: ${status}`,
        ...(dropped > 0 ? [`전송 한도 초과로 ${dropped}줄 누락`] : []),
      ],
    },
    lines.map((line) => `${logTime(line.at)} ${field(line.stream)} ${cleanText(line.text)}`),
    logFileName(header.server, ['logs', service], header.demo, now),
    now,
  )
}

export function downloadLog(file: LogFile): void {
  const url = URL.createObjectURL(new Blob([file.text], { type: 'text/plain;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = file.name
  document.body.appendChild(link)
  link.click()
  link.remove()
  // 내려받기가 시작할 시간을 준 뒤 해제한다.
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
