import { useEffect, useMemo, useRef } from 'react'
import { useQueries } from '@tanstack/react-query'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { fetchSamples, type Collector, type EventRow, type GapRow, type SampleSeries, type Server, type Service, type Topology } from '../api'
import { alignSeries, chartPoints, hasAnyValue, type AlignedData } from '../chartData'
import { parseTarget, timeMode, timeWindow, updateContext, type ConsoleContext } from '../context'
import { formatBytes, formatClock, formatPercent, formatRate } from '../format'
import { serviceLabel } from '../status'
import { UnsupportedIcon } from './Icons'

const SERIES_COLORS = ['#1f4fd8', '#0f8a8a', '#7a2db8', '#b0620a']
const SAMPLE_REFRESH_MS = 15_000

interface SeriesSpec {
  scope: string
  name: string
  label: string
}

interface ChartSpec {
  id: string
  title: string
  format: (value: number) => string
  series: SeriesSpec[]
  collector?: string
}

function hostCharts(): ChartSpec[] {
  const host = (name: string, label: string): SeriesSpec => ({ scope: 'host', name, label })
  return [
    { id: 'cpu', title: 'CPU', format: formatPercent, collector: 'cpu', series: [host('host.cpu.percent', '사용률')] },
    { id: 'mem', title: '메모리', format: formatPercent, collector: 'memory', series: [host('host.mem.percent', '사용률')] },
    { id: 'disk', title: '디스크 사용량', format: formatPercent, collector: 'disk', series: [host('host.disk.percent', '사용률')] },
    { id: 'net', title: '네트워크', format: formatRate, collector: 'network', series: [host('host.net.rx_bps', '수신'), host('host.net.tx_bps', '송신')] },
    { id: 'io', title: '디스크 I/O', format: formatRate, collector: 'disk_io', series: [host('host.disk.read_bps', '읽기'), host('host.disk.write_bps', '쓰기')] },
    { id: 'temp', title: '온도', format: (v) => `${v.toFixed(0)}°C`, collector: 'temperature', series: [host('host.temperature', '온도')] },
  ]
}

function serviceCharts(service: Service): ChartSpec[] {
  const scopes = service.containers.map((container) => ({
    scope: `${service.key}#${container.replica}`,
    suffix: service.containers.length > 1 ? ` #${container.replica}` : '',
  }))
  return [
    { id: 'cpu', title: 'CPU', format: formatPercent, series: scopes.map(({ scope, suffix }) => ({ scope, name: 'container.cpu.percent', label: `사용률${suffix}` })) },
    { id: 'mem', title: '메모리', format: formatBytes, series: scopes.map(({ scope, suffix }) => ({ scope, name: 'container.mem.bytes', label: `사용량${suffix}` })) },
    {
      id: 'net',
      title: '네트워크',
      format: formatRate,
      series: scopes.flatMap(({ scope, suffix }) => [
        { scope, name: 'container.net.rx_bps', label: `수신${suffix}` },
        { scope, name: 'container.net.tx_bps', label: `송신${suffix}` },
      ]),
    },
    {
      id: 'latency',
      title: '응답 시간',
      format: (v) => `${v.toFixed(0)}ms`,
      series: [
        { scope: service.key, name: 'probe.internal.latency_ms', label: '내부' },
        { scope: service.key, name: 'probe.external.latency_ms', label: '외부' },
      ],
    },
  ]
}

interface ChartProps {
  spec: ChartSpec
  data: AlignedData
  gaps: GapRow[]
  cursorAt?: number
}

function TimeChart({ spec, data, gaps, cursorAt }: ChartProps) {
  const box = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  const overlay = useRef({ gaps, cursorAt })
  overlay.current = { gaps, cursorAt }

  useEffect(() => {
    const element = box.current
    if (!element) return
    const options: uPlot.Options = {
      width: element.clientWidth,
      height: element.clientHeight,
      legend: { show: false },
      cursor: { drag: { x: false, y: false } },
      scales: { x: { time: true }, y: { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] } },
      axes: [
        {
          size: 20,
          font: '10px ui-monospace, monospace',
          stroke: '#52608c',
          grid: { stroke: '#eef1f9', width: 1 },
          ticks: { show: false },
          values: (_u, values) => values.map((value) => formatClock(value * 1000).slice(0, 5)),
        },
        { size: 58, font: '10px ui-monospace, monospace', stroke: '#52608c', grid: { stroke: '#eef1f9', width: 1 }, ticks: { show: false }, values: (_u, values) => values.map(spec.format) },
      ],
      series: [
        {},
        ...spec.series.map((item, index) => ({
          label: item.label,
          stroke: SERIES_COLORS[index % SERIES_COLORS.length],
          width: 1.3,
          points: { show: false },
          spanGaps: false,
        })),
      ],
      hooks: {
        draw: [
          (u) => {
            const { ctx, bbox } = u
            ctx.save()
            for (const gap of overlay.current.gaps) {
              const left = u.valToPos(Date.parse(gap.from) / 1000, 'x', true)
              const right = u.valToPos(Date.parse(gap.to) / 1000, 'x', true)
              ctx.fillStyle = 'rgba(90, 100, 128, 0.14)'
              ctx.fillRect(left, bbox.top, right - left, bbox.height)
            }
            if (overlay.current.cursorAt) {
              const x = u.valToPos(overlay.current.cursorAt / 1000, 'x', true)
              ctx.strokeStyle = '#1f4fd8'
              ctx.setLineDash([4, 3])
              ctx.beginPath()
              ctx.moveTo(x, bbox.top)
              ctx.lineTo(x, bbox.top + bbox.height)
              ctx.stroke()
            }
            ctx.restore()
          },
        ],
      },
    }
    const chart = new uPlot(options, [[], ...spec.series.map(() => [])], element)
    plot.current = chart
    const observer = new ResizeObserver(() => chart.setSize({ width: element.clientWidth, height: element.clientHeight }))
    observer.observe(element)
    return () => {
      observer.disconnect()
      chart.destroy()
      plot.current = null
    }
    // 차트 구성은 지표 종류가 바뀔 때만 다시 만든다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spec.id, spec.series.length])

  useEffect(() => {
    plot.current?.setData([data.times.map((t) => t / 1000), ...data.values] as uPlot.AlignedData)
  }, [data])

  useEffect(() => {
    plot.current?.redraw(false)
  }, [gaps, cursorAt])

  return <div className="chart-box" ref={box} data-chart={spec.id} />
}

function emptyReason(collector?: Collector): { failed: boolean; node: React.ReactNode } {
  if (collector?.state === 'unsupported') {
    return { failed: false, node: <><UnsupportedIcon /> 미지원: {collector.error || '이 환경에서 수집할 수 없습니다'}</> }
  }
  if (collector?.state === 'error') {
    return { failed: true, node: <>수집 실패: {collector.error || '수집 중 오류가 발생했습니다'}</> }
  }
  return { failed: false, node: '이 구간에 데이터가 없습니다' }
}

function ChartCard({ spec, data, gaps, cursorAt, collector, loading }: ChartProps & { collector?: Collector; loading: boolean }) {
  const hasData = hasAnyValue(data)
  // 값이 있어도 지금 수집이 실패 중이면 함께 알린다.
  const reason = emptyReason(collector)
  return (
    <div>
      <div className="chart-title">
        {spec.title}
        {collector?.state === 'error' && hasData && <span className="collect-failed">현재 수집 실패</span>}
        <span className="chart-legend">
          {spec.series.map((item, index) => (
            <span key={item.label} style={{ color: SERIES_COLORS[index % SERIES_COLORS.length], marginRight: 6 }}>— {item.label}</span>
          ))}
        </span>
      </div>
      {hasData ? (
        <TimeChart spec={spec} data={data} gaps={gaps} cursorAt={cursorAt} />
      ) : (
        <div className={`chart-empty ${reason.failed ? 'failed' : ''}`} data-chart-empty={spec.id}>{loading ? '불러오는 중' : reason.node}</div>
      )}
    </div>
  )
}

interface UsageRow {
  service: Service
  cpu: number | undefined
  mem: number | undefined
}

// ResourceUsage는 자원을 많이 쓰는 서비스를 찾기 위한 목록이다.
function ResourceUsage({ topology, target }: { topology?: Topology; target?: string }) {
  const rows = useMemo<UsageRow[]>(() => {
    const services = topology?.projects.flatMap((project) => project.services) ?? []
    return services
      .map((service) => {
        const sum = (name: string) => {
          const values = service.containers.map((container) => container.metrics[name]).filter(Boolean)
          return values.length > 0 ? values.reduce((total, value) => total + value.value, 0) : undefined
        }
        return { service, cpu: sum('container.cpu.percent'), mem: sum('container.mem.bytes') }
      })
      .sort((a, b) => (b.cpu ?? -1) - (a.cpu ?? -1))
  }, [topology])
  const show = (value: number | undefined, format: (v: number) => string) => (value === undefined ? '—' : format(value))
  return (
    <div className="usage">
      <h3>서비스별 자원 (CPU 순)</h3>
      {rows.map(({ service, cpu, mem }) => (
        <button
          key={service.key}
          type="button"
          className={`usage-row ${target === `svc:${service.key}` ? 'selected' : ''}`}
          onClick={() => updateContext({ target: `svc:${service.key}` })}
        >
          <span className="mono ellipsis">{serviceLabel(service.key)}</span>
          <span className="mono num">{show(cpu, formatPercent)}</span>
          <span className="mono num">{show(mem, formatBytes)}</span>
        </button>
      ))}
      <p className="muted small">—는 실행 중이 아니거나 최근 값이 없는 경우입니다.</p>
    </div>
  )
}

interface Props {
  server: Server
  topology?: Topology
  context: ConsoleContext
  gaps: GapRow[]
  selectedEvent?: EventRow
  now: number
}

export function MetricsPanel({ server, topology, context, gaps, selectedEvent, now }: Props) {
  const parsed = parseTarget(context.target)
  const service = parsed?.kind === 'service'
    ? topology?.projects.flatMap((project) => project.services).find((item) => item.key === parsed.key)
    : undefined
  const charts = useMemo(() => (service ? serviceCharts(service) : hostCharts()), [service])

  const live = timeMode(context) === 'live'
  const fetchKeys = charts.flatMap((chart) => chart.series.map((series) => `${series.scope}|${series.name}`))
  const windowKey = live ? context.range : `${context.from}|${context.to}|${context.frozen}|${context.range}`

  const queries = useQueries({
    queries: charts.flatMap((chart) =>
      Array.from(new Set(chart.series.map((series) => series.scope))).map((scope) => ({
        queryKey: ['samples', server.id, chart.id, scope, fetchKeys.join(','), windowKey],
        queryFn: () => {
          const { from, to } = timeWindow(context, Date.now())
          const names = chart.series.filter((series) => series.scope === scope).map((series) => series.name)
          return fetchSamples(server.id, scope, names, from, to, chartPoints(Date.parse(from), Date.parse(to)))
        },
        refetchInterval: live ? SAMPLE_REFRESH_MS : false,
        placeholderData: (previous: Awaited<ReturnType<typeof fetchSamples>> | undefined) => previous,
      })),
    ),
  })

  let cursor = 0
  const { from, to } = timeWindow(context, now)
  const fromMs = Date.parse(from)
  const toMs = Date.parse(to)
  const prepared = charts.map((chart) => {
    const scopes = Array.from(new Set(chart.series.map((series) => series.scope)))
    const results = scopes.map(() => queries[cursor++])
    const merged: SampleSeries[] = chart.series.map((series) => {
      const result = results[scopes.indexOf(series.scope)]
      return result.data?.series.find((item) => item.name === series.name) ?? { name: series.name, points: [] }
    })
    const step = results[0]?.data?.step_ms ?? 60_000
    return {
      chart,
      data: alignSeries(merged, fromMs, toMs, step),
      loading: results.some((result) => result.isLoading),
    }
  })

  const cursorAt = selectedEvent ? Date.parse(selectedEvent.occurred_at) : undefined

  return (
    <div className="metrics">
      <div className="charts">
        {prepared.map(({ chart, data, loading }) => (
          <ChartCard
            key={chart.id}
            spec={chart}
            data={data}
            gaps={gaps}
            cursorAt={cursorAt}
            loading={loading}
            collector={chart.collector ? server.collectors.find((item) => item.name === chart.collector) : undefined}
          />
        ))}
      </div>
      <ResourceUsage topology={topology} target={context.target} />
    </div>
  )
}
