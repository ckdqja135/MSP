import { memo, useEffect, useMemo, useRef, useState } from 'react'
import {
  EdgeLabelRenderer,
  Handle,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  useStore,
  useViewport,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { diffActivities, eventActivity, mergeCallouts, type Activity } from '../activity'
import type { EventRow, HostService, Network, Project, Server, Service, Topology } from '../api'
import { updateContext } from '../context'
import type { PacketKind } from '../flow'
import { compactPorts, formatBytes, formatPercent, formatUptime, readMetric } from '../format'
import {
  CELL_X,
  CELL_Y,
  assignSlots,
  collapsedProjects,
  computeScene,
  emptySlots,
  hopRoute,
  hostServiceRole,
  routeBetween,
  serviceRole,
  visibleDevice,
  type Point,
  type SceneDevice,
  type ScenePlate,
  type SlotMemory,
} from '../scene'
import { HOST_EVIDENCE_TAG, STATUS_LABEL, hostServiceName, projectLabel, serviceHeadline } from '../status'
import type { FlowPlayer } from '../useFlowPlayer'
import { StatusIcon, UnsupportedIcon } from './Icons'
import { CloudIcon, HostIcon, ServiceIcon, StackIcon, SwitchIcon } from './IsoIcons'

export interface MapHighlight {
  nodes: ReadonlySet<string>
  note?: string
}

interface Layers {
  ports: boolean
  networks: boolean
  declared: boolean
  activity: boolean
}

// 장비 상자의 크기와 바닥 중심 위치다. 연결선은 바닥 중심에서 끝난다.
const DEVICE_BOX = {
  service: { width: 300, height: 100, anchorX: 42, anchorY: 66 },
  switch: { width: 260, height: 66, anchorX: 39, anchorY: 42 },
  host: { width: 250, height: 196, anchorX: 58, anchorY: 164 },
  internet: { width: 260, height: 92, anchorX: 62, anchorY: 50 },
  stack: { width: 300, height: 100, anchorX: 46, anchorY: 70 },
  hostService: { width: 280, height: 100, anchorX: 42, anchorY: 66 },
} as const

const CALLOUT = { width: 236, height: 86, offsetX: 34, offsetY: 138 }
const MAX_CALLOUTS = 6
const ENTER_ANIMATION_MS = 2400
const STAGE_DELAY_MS = 260
const FIT_PADDING = { top: '48px', bottom: '40px', left: '16px', right: '16px' } as const

function loadJSON<T>(key: string, fallback: T): T {
  try {
    const text = window.localStorage.getItem(key)
    return text ? (JSON.parse(text) as T) : fallback
  } catch {
    return fallback
  }
}

function serviceTotals(service: Service) {
  let cpu = 0
  let mem = 0
  let cpuAt: string | undefined
  let memAt: string | undefined
  for (const container of service.containers) {
    const c = container.metrics['container.cpu.percent']
    const m = container.metrics['container.mem.bytes']
    if (c) {
      cpu += c.value
      cpuAt = c.at
    }
    if (m) {
      mem += m.value
      memAt = m.at
    }
  }
  return {
    cpu: cpuAt ? { value: cpu, at: cpuAt } : undefined,
    mem: memAt ? { value: mem, at: memAt } : undefined,
  }
}

type DeviceFlags = {
  selected: boolean
  highlighted: boolean
  dimmed: boolean
  // 재생 중 패킷이 아직 지나가지 않은 장비다.
  flowHidden: boolean
  entering: boolean
  enterDelay: number
}

function deviceClasses(base: string, flags: DeviceFlags, extra?: string): string {
  return [base, extra, flags.selected && 'selected', flags.highlighted && 'highlighted', flags.dimmed && 'dimmed', flags.flowHidden && 'flow-hidden', flags.entering && 'entering']
    .filter(Boolean)
    .join(' ')
}

function Handles() {
  return (
    <>
      <Handle type="target" position={Position.Left} className="hidden-handle" />
      <Handle type="source" position={Position.Right} className="hidden-handle" />
    </>
  )
}

type ServiceData = DeviceFlags & { service: Service; now: number }

const ServiceNode = memo(function ServiceNode({ data }: NodeProps<Node<ServiceData, 'service'>>) {
  const { service, now } = data
  const container = service.containers[0]
  const address = container?.networks?.find((network) => network.ip)?.ip
  const ports = compactPorts(service.containers.flatMap((c) => c.ports ?? []))
  const totals = serviceTotals(service)
  const cpu = readMetric(totals.cpu, now, formatPercent)
  const mem = readMetric(totals.mem, now, formatBytes)
  const stopped = service.runtime === 'stopped'
  const abnormal = service.status !== 'ok'
  return (
    <div
      className={deviceClasses('device service-card', data, `${service.status} role-${serviceRole(service)}`)}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <ServiceIcon role={serviceRole(service)} />
      <div className="device-label">
        <div className="device-name">
          <strong>{service.name}</strong>
          {service.containers.length > 1 && <span className="muted"> ×{service.containers.length}</span>}
          <span className={`badge ${service.status}`}>
            <StatusIcon status={service.status} size={11} />
            {STATUS_LABEL[service.status]}
          </span>
        </div>
        <div className="device-address">
          {stopped ? '실행 중 아님' : (address ?? '주소 없음')}
          {ports && <span className="device-ports"> · {ports}</span>}
        </div>
        {abnormal && <div className="device-state">{serviceHeadline(service)}</div>}
        {!stopped && (
          <div className="device-metrics">
            <span className={cpu.state === 'stale' ? 'stale' : ''}>CPU {cpu.text}</span>
            <span className={mem.state === 'stale' ? 'stale' : ''}> · {mem.text}</span>
          </div>
        )}
      </div>
    </div>
  )
})

type HostServiceData = DeviceFlags & { hostService: HostService }

// 호스트 서비스는 이름의 근거를 함께 표시한다. 추정을 확인된 사실처럼 보이지 않게 하기 위해서다.
const HostServiceNode = memo(function HostServiceNode({ data }: NodeProps<Node<HostServiceData, 'hostService'>>) {
  const { hostService } = data
  const ports = [...new Set(hostService.ports.map((port) => port.port))].sort((a, b) => a - b)
  const status = hostService.listening ? 'ok' : 'critical'
  const shown = ports.length > 3 ? `${ports.slice(0, 3).join(', ')} 외 ${ports.length - 3}개` : ports.join(', ')
  return (
    <div
      className={deviceClasses('device host-service-card', data, status)}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <ServiceIcon role={hostServiceRole(hostService)} />
      <div className="device-label">
        <div className="device-name">
          <strong>{hostServiceName(hostService)}</strong>
          <span className={`evidence-tag ${hostService.evidence}`}>{HOST_EVIDENCE_TAG[hostService.evidence]}</span>
        </div>
        <div className="device-address">포트 {shown}</div>
        <div className={hostService.listening ? 'device-metrics' : 'device-state'}>{hostService.listening ? '열림' : '포트 닫힘'}</div>
      </div>
    </div>
  )
})

type StackData = DeviceFlags & { project: Project; now: number }

// 접힌 프로젝트는 장비 하나로 그리고 상태 집계와 자원 합계를 보여준다.
const StackNode = memo(function StackNode({ data }: NodeProps<Node<StackData, 'stack'>>) {
  const { project, now } = data
  const counts = { ok: 0, warning: 0, critical: 0, stopped: 0, disconnected: 0 }
  let cpu = 0
  let mem = 0
  let measured = false
  for (const service of project.services) {
    counts[service.status]++
    const totals = serviceTotals(service)
    if (totals.cpu && readMetric(totals.cpu, now, formatPercent).state === 'fresh') {
      cpu += totals.cpu.value
      measured = true
    }
    if (totals.mem && readMetric(totals.mem, now, formatBytes).state === 'fresh') mem += totals.mem.value
  }
  const parts = [
    counts.critical > 0 && `장애 ${counts.critical}`,
    counts.warning > 0 && `주의 ${counts.warning}`,
    counts.ok > 0 && `정상 ${counts.ok}`,
    counts.stopped > 0 && `중지 ${counts.stopped}`,
    counts.disconnected > 0 && `끊김 ${counts.disconnected}`,
  ].filter(Boolean)
  return (
    <div
      className={deviceClasses('device stack-card', data, project.status)}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <StackIcon />
      <div className="device-label">
        <div className="device-name">
          <strong>{projectLabel(project.key)}</strong>
          <span className={`badge ${project.status}`}>
            <StatusIcon status={project.status} size={11} />
            {STATUS_LABEL[project.status]}
          </span>
        </div>
        <div className="device-address">서비스 {project.services.length}개 · {parts.join(' · ')}</div>
        {measured ? (
          <div className="device-metrics">CPU {formatPercent(cpu)} · {formatBytes(mem)}</div>
        ) : (
          <div className="device-metrics">지표 없음</div>
        )}
        <div className="stack-hint">눌러서 펼치기</div>
      </div>
    </div>
  )
})

type HostData = DeviceFlags & { server: Server; now: number }

const HostNode = memo(function HostNode({ data }: NodeProps<Node<HostData, 'host'>>) {
  const { server, now } = data
  const metric = (name: string, format: (value: number) => string) => readMetric(server.metrics[name], now, format).text
  const temperature = server.collectors.find((c) => c.name === 'temperature')
  const reachable = server.reachability.state === 'ok'
  return (
    <div
      className={deviceClasses('device host-card', data, reachable ? 'ok' : 'disconnected')}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <div className="device-label">
        <div className="device-name host-name">
          <strong>{server.id}</strong>
        </div>
        <div className="device-address">서버 · 가동 {formatUptime(server.uptime_seconds)}</div>
        {!reachable && <div className="device-state">{server.reachability.label}</div>}
        <div className="device-metrics">
          CPU {metric('host.cpu.percent', formatPercent)} · MEM {metric('host.mem.percent', formatPercent)}
        </div>
        <div className="device-metrics">
          DISK {metric('host.disk.percent', formatPercent)} ·{' '}
          {temperature?.state === 'unsupported' ? (
            <span className="muted">
              <UnsupportedIcon /> 온도 미지원
            </span>
          ) : (
            <>온도 {metric('host.temperature', (v) => `${v.toFixed(0)}°C`)}</>
          )}
        </div>
      </div>
      <HostIcon />
    </div>
  )
})

type SwitchData = DeviceFlags & { network: Network }

const SwitchNode = memo(function SwitchNode({ data }: NodeProps<Node<SwitchData, 'switch'>>) {
  const { network } = data
  return (
    <div
      className={deviceClasses('device switch-card', data)}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <SwitchIcon />
      <div className="device-label">
        <div className="device-name network-name">{network.name}</div>
        <div className="device-address">{network.subnet ?? '서브넷 없음'}</div>
      </div>
    </div>
  )
})

type InternetData = DeviceFlags

const InternetNode = memo(function InternetNode({ data }: NodeProps<Node<InternetData, 'internet'>>) {
  return (
    <div
      className={deviceClasses('device internet-card', data)}
      style={{ '--enter-delay': `${data.enterDelay}ms` } as React.CSSProperties}
    >
      <Handles />
      <CloudIcon />
      <div className="device-label">
        <div className="device-name network-name">외부</div>
        <div className="device-address small-note" title="공개 포트 바인딩 기준입니다. 실제 도달 여부는 외부 응답 확인으로 알 수 있습니다">공개 포트 기준</div>
      </div>
    </div>
  )
})

type PlateData = { plate: ScenePlate; project?: Project; selected: boolean; collapsed: boolean; onToggle: () => void; origin: Point }

// 프로젝트 판은 시각적 묶음이다. 머리글만 클릭을 받고 나머지는 뒤의 장비와 화면 이동을 막지 않는다.
const PlateNode = memo(function PlateNode({ data }: NodeProps<Node<PlateData, 'plate'>>) {
  const { plate, project, selected, collapsed, onToggle, origin } = data
  const points = plate.corners.map((corner) => `${corner.x - origin.x},${corner.y - origin.y}`).join(' ')
  // 머리글은 판의 위 꼭짓점 위에 둔다. 옆 프로젝트의 라벨과 겹치지 않는 자리다.
  const apex = plate.corners[1]
  if (plate.host || !project) {
    // 호스트 판 위쪽은 스위치와 연결선이 지나가므로 아래 꼭짓점 안쪽에 적는다. 판 밖에 두면 범례에 가려진다.
    const bottom = plate.corners[3]
    return (
      <div className="project-plate host-plate">
        <svg className="plate-shape" aria-hidden="true">
          <polygon points={points} />
        </svg>
        <div className="plate-head plate-head-below" style={{ left: bottom.x - origin.x, top: bottom.y - origin.y - 26 }}>
          <strong>이 서버, Docker 밖</strong>
        </div>
      </div>
    )
  }
  return (
    <div className={`project-plate ${selected ? 'selected' : ''}`}>
      <svg className="plate-shape" aria-hidden="true">
        <polygon points={points} />
      </svg>
      <div className="plate-head" style={{ left: apex.x - origin.x, top: apex.y - origin.y - 4 }}>
        <button
          type="button"
          className="icon-button nodrag"
          aria-label={collapsed ? '그룹 펼치기' : '그룹 접기'}
          aria-expanded={!collapsed}
          onClick={(event) => {
            event.stopPropagation()
            onToggle()
          }}
        >
          {collapsed ? '▸' : '▾'}
        </button>
        <strong>{projectLabel(project.key)}</strong>
        <span className="muted mono">{project.services.length}</span>
        {plate.hiddenJobs > 0 && <span className="muted hidden-jobs" title="끝난 일회성 작업은 그림에서 숨기고 왼쪽 목록에 표시합니다">+{plate.hiddenJobs} 작업</span>}
        <span className={`badge ${project.status}`}>
          <StatusIcon status={project.status} size={11} />
          {STATUS_LABEL[project.status]}
        </span>
      </div>
    </div>
  )
})

type CalloutData = { activity: Activity }

// 설명 카드는 실제로 일어난 일에만 붙는다.
const CalloutNode = memo(function CalloutNode({ data }: NodeProps<Node<CalloutData, 'callout'>>) {
  const { activity } = data
  return (
    <div className={`callout ${activity.tone}`} role="status">
      <Handles />
      <div className="callout-title">{activity.title}</div>
      {activity.lines.map((line) => (
        <div key={line} className="callout-line">{line}</div>
      ))}
    </div>
  )
})

const nodeTypes = {
  service: ServiceNode,
  hostService: HostServiceNode,
  stack: StackNode,
  host: HostNode,
  switch: SwitchNode,
  internet: InternetNode,
  plate: PlateNode,
  callout: CalloutNode,
}

type LinkData = {
  d: string
  label?: string
  labelAt?: Point
  marker?: string
  entering?: boolean
  enterDelay?: number
  dot?: boolean
}

// IsoEdge는 축을 따라 미리 계산한 경로를 그린다. 새로 생긴 선은 앞에서부터 한 번 그려진다.
function IsoEdge({ id, data }: EdgeProps<Edge<LinkData, 'iso'>>) {
  const { d, label, labelAt, marker, entering, enterDelay, dot } = data!
  const maskID = `draw-${id.replace(/[^a-zA-Z0-9]/g, '-')}`
  return (
    <g>
      {entering && (
        <mask id={maskID} maskUnits="userSpaceOnUse" x="-6000" y="-6000" width="12000" height="12000">
          <path d={d} className="draw-mask" pathLength={1} style={{ animationDelay: `${enterDelay ?? 0}ms` }} />
        </mask>
      )}
      <path
        d={d}
        className="react-flow__edge-path"
        fill="none"
        mask={entering ? `url(#${maskID})` : undefined}
        markerEnd={marker ? `url(#${marker})` : undefined}
      />
      {dot && (
        <circle className="activity-dot" r="5">
          <animateMotion dur="1.3s" path={d} fill="freeze" />
        </circle>
      )}
      {label && labelAt && (
        <text className="edge-label" x={labelAt.x} y={labelAt.y - 8} textAnchor="middle">
          {label}
        </text>
      )}
    </g>
  )
}

type PacketData = {
  d: string
  reverse: boolean
  kind: PacketKind
  title: string
  detail: string
  protocol: string
  example: boolean
  progress: () => number
}

// PacketEdge는 움직이는 패킷과 내용 카드를 그린다. 매 프레임 바뀌므로 React를 거치지 않고 직접 옮긴다.
function PacketEdge({ data }: EdgeProps<Edge<PacketData, 'packet'>>) {
  const { d, reverse, kind, title, detail, protocol, example, progress } = data!
  const route = useRef<SVGPathElement>(null)
  const trail = useRef<SVGPathElement>(null)
  const dot = useRef<SVGCircleElement>(null)
  const card = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let raf = 0
    const tick = () => {
      const element = route.current
      if (element) {
        const length = element.getTotalLength()
        const passed = length * progress()
        const at = element.getPointAtLength(reverse ? length - passed : passed)
        dot.current?.setAttribute('cx', String(at.x))
        dot.current?.setAttribute('cy', String(at.y))
        if (trail.current) {
          trail.current.style.strokeDasharray = `${passed} ${length}`
          trail.current.style.strokeDashoffset = reverse ? String(passed - length) : '0'
        }
        if (card.current) card.current.style.transform = `translate(-50%, -100%) translate(${at.x}px, ${at.y - 14}px)`
      }
      raf = requestAnimationFrame(tick)
    }
    tick()
    return () => cancelAnimationFrame(raf)
  }, [d, reverse, progress])

  return (
    <g className={`packet packet-${kind}`}>
      <path ref={route} d={d} fill="none" stroke="none" />
      <path ref={trail} d={d} fill="none" className="packet-trail" />
      <circle ref={dot} className="packet-dot" r="6" />
      <EdgeLabelRenderer>
        <div ref={card} className={`packet-card packet-${kind}`}>
          <div className="packet-card-title">
            <span>{protocol}</span>
            {example && <em>예시</em>}
          </div>
          <div className="packet-card-main">{title}</div>
          <div className="packet-card-detail">{detail}</div>
        </div>
      </EdgeLabelRenderer>
    </g>
  )
}

const edgeTypes = { iso: IsoEdge, packet: PacketEdge }

// IsoGrid는 장비와 함께 움직이는 등각 격자다.
function IsoGrid() {
  const { x, y, zoom } = useViewport()
  return (
    <svg className="iso-grid" aria-hidden="true">
      <defs>
        <pattern id="iso-grid-tile" width={CELL_X} height={CELL_Y} patternUnits="userSpaceOnUse" patternTransform={`translate(${x} ${y}) scale(${zoom})`}>
          <path d={`M0 0L${CELL_X} ${CELL_Y}M0 ${CELL_Y}L${CELL_X} 0`} />
        </pattern>
      </defs>
      <rect width="100%" height="100%" fill="url(#iso-grid-tile)" />
    </svg>
  )
}

const LINK_MARKER: Record<string, string | undefined> = {
  external: 'iso-arrow',
  publish: 'iso-arrow',
  loopback: 'iso-arrow-muted',
  declared: 'iso-arrow-muted',
}

interface MapProps {
  server: Server
  topology: Topology
  target?: string
  highlight: MapHighlight
  now: number
  live: boolean
  selectedEvent?: EventRow
  focus: boolean
  // 패널을 열고 닫으면 맵 크기가 바뀌므로 다시 맞춘다. 장비의 위치는 바뀌지 않는다.
  layoutKey: string
  player?: FlowPlayer
  // 어느 탭에 있든 맵에서 바로 재생을 시작한다.
  flowPlayLabel: string
  onFlowPlay?: () => void
}

// 이 배율보다 작으면 이름과 상태만 크게 보여준다. 작은 글자는 어차피 읽히지 않는다.
const FAR_ZOOM = 0.62
const FLOW_PADDING = { top: '150px', bottom: '70px', left: '110px', right: '110px' } as const
const FLOW_CAMERA_MS = 620

function MapCanvas({ server, topology, target, highlight, now, live, selectedEvent, focus, layoutKey, player, flowPlayLabel, onFlowPlay }: MapProps) {
  const { fitView } = useReactFlow()
  const slotKey = `msp.slots.${server.mode}.${server.id}`
  const collapseKey = `msp.collapse-overrides.${server.mode}.${server.id}`
  const [slots, setSlots] = useState<SlotMemory>(() => loadJSON(slotKey, emptySlots()))
  const [collapseOverrides, setCollapseOverrides] = useState<Record<string, boolean>>(() => loadJSON(collapseKey, {}))
  const [layers, setLayers] = useState<Layers>({ ports: true, networks: true, declared: false, activity: true })

  const currentSlots = useMemo(() => assignSlots(slots, topology), [slots, topology])
  useEffect(() => {
    if (JSON.stringify(currentSlots) !== JSON.stringify(slots)) {
      setSlots(currentSlots)
      window.localStorage.setItem(slotKey, JSON.stringify(currentSlots))
    }
  }, [currentSlots, slots, slotKey])

  const collapsedSet = useMemo(() => collapsedProjects(topology, collapseOverrides), [topology, collapseOverrides])
  const scene = useMemo(() => computeScene(topology, currentSlots, collapsedSet), [topology, currentSlots, collapsedSet])

  const setProjectCollapsed = (key: string, value: boolean) => {
    const next = { ...collapseOverrides, [key]: value }
    setCollapseOverrides(next)
    window.localStorage.setItem(collapseKey, JSON.stringify(next))
  }
  const toggleCollapsed = (key: string) => setProjectCollapsed(key, !collapsedSet.has(key))

  const userMoved = useRef(false)
  const fitAll = () => fitView({ padding: FIT_PADDING, duration: 450 })

  const reset = () => {
    const fresh = assignSlots(emptySlots(), topology)
    setSlots(fresh)
    window.localStorage.setItem(slotKey, JSON.stringify(fresh))
    userMoved.current = false
    window.setTimeout(fitAll, 60)
  }

  const zoom = useStore((state) => state.transform[2])

  // 알림에서 들어오면 대상 장비로 화면을 옮긴다.
  useEffect(() => {
    if (!focus || !target) return
    userMoved.current = true
    const device = visibleDevice(scene, target) ?? target
    const timer = window.setTimeout(() => fitView({ nodes: [{ id: device }], maxZoom: 1.1, padding: 1.1, duration: 450 }), 300)
    return () => window.clearTimeout(timer)
    // 대상이 바뀔 때만 옮긴다. 데이터 갱신으로 화면이 움직이면 안 된다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, target, fitView])

  const layoutState = useRef(layoutKey)
  useEffect(() => {
    if (layoutState.current === layoutKey) return
    layoutState.current = layoutKey
    if (userMoved.current) return
    const timer = window.setTimeout(fitAll, 80)
    return () => window.clearTimeout(timer)
    // 패널 상태가 바뀔 때만 실행한다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [layoutKey])

  // 새로 나타난 장비와 선을 찾아 한 번만 그리는 효과를 준다. 사용자가 화면을 옮기지 않았다면 전체가 보이게 맞춘다.
  const seen = useRef<Set<string> | null>(null)
  const [entering, setEntering] = useState<ReadonlySet<string>>(new Set())
  const firstDraw = useRef(true)
  const sceneKey = useMemo(
    () => [...scene.devices.map((device) => device.id), ...scene.links.map((link) => link.id)].sort().join('|'),
    [scene],
  )
  useEffect(() => {
    const ids = sceneKey.split('|')
    const isFirst = seen.current === null
    const known: Set<string> = seen.current ?? new Set<string>()
    const fresh = ids.filter((id) => !known.has(id))
    seen.current = new Set([...known, ...ids])
    if (fresh.length === 0) return
    setEntering(new Set(fresh))
    firstDraw.current = isFirst
    const newDevice = fresh.some((id) => scene.positions[id])
    const fit = !isFirst && newDevice && !userMoved.current ? window.setTimeout(fitAll, 60) : undefined
    const clear = window.setTimeout(() => setEntering(new Set()), ENTER_ANIMATION_MS)
    return () => {
      window.clearTimeout(clear)
      if (fit) window.clearTimeout(fit)
    }
    // 장비와 선 목록이 바뀔 때만 실행한다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sceneKey])

  // 구조 데이터가 갱신될 때 실제로 일어난 변화를 찾아 그린다.
  const previousTopology = useRef<Topology | null>(null)
  const [activities, setActivities] = useState<Activity[]>([])
  useEffect(() => {
    if (live && previousTopology.current) {
      const fresh = diffActivities(previousTopology.current, topology, Date.now())
      if (fresh.length > 0) setActivities((current) => [...current, ...fresh])
    }
    previousTopology.current = topology
  }, [topology, live])
  useEffect(() => {
    setActivities((current) => {
      const alive = current.filter((activity) => activity.until > Date.now())
      return alive.length === current.length ? current : alive
    })
  }, [now])

  const selectedActivity = useMemo(() => (selectedEvent ? eventActivity(selectedEvent) : undefined), [selectedEvent])
  const shown = useMemo(() => {
    const list = layers.activity ? activities : []
    return selectedActivity ? [selectedActivity, ...list] : list
  }, [activities, selectedActivity, layers.activity])

  // 재생 중에는 패킷이 지나간 장비만 또렷하게 보이고, 화면은 지금 패킷의 경로를 따라간다.
  const playing = Boolean(player && player.status !== 'idle' && player.frame)
  const frame = playing ? player!.frame : undefined
  const flowRevealed = useMemo(
    () => new Set((frame?.revealed ?? []).map((id) => visibleDevice(scene, id) ?? id)),
    [frame?.revealed, scene],
  )
  const cameraKey = frame ? frame.camera.join('|') : ''
  useEffect(() => {
    if (!playing || !frame) return
    userMoved.current = true
    const ids = [...new Set(frame.camera.map((id) => visibleDevice(scene, id)).filter((id): id is string => Boolean(id)))]
    if (ids.length > 0) fitView({ nodes: ids.map((id) => ({ id })), padding: FLOW_PADDING, maxZoom: 1.05, duration: FLOW_CAMERA_MS })
    // 경로가 바뀔 때만 화면을 옮긴다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraKey, playing])
  const wasPlaying = useRef(false)
  useEffect(() => {
    if (wasPlaying.current && !playing) {
      userMoved.current = false
      window.setTimeout(fitAll, 60)
    }
    wasPlaying.current = playing
    // 재생이 끝나 개요로 돌아올 때만 다시 맞춘다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playing])

  const currentPacket = frame && frame.phase !== 'overview' && frame.phase !== 'intro' && frame.phase !== 'done' ? player!.timeline?.packets[frame.packet] : undefined

  const selectedNetwork = target?.startsWith('net:') ? target.slice(4) : undefined
  // 네트워크를 선택하면 소속 서비스를 강조한다. 소속만 뜻하고 호출 관계는 뜻하지 않는다.
  const highlightedNodes = useMemo(() => {
    const members = topology.networks.find((network) => network.name === selectedNetwork)?.members ?? []
    return new Set([...highlight.nodes, ...members.map((member) => `svc:${member}`)])
  }, [highlight.nodes, selectedNetwork, topology.networks])
  const hasFocus = highlightedNodes.size > 0

  const nodes = useMemo(() => {
    const result: Node[] = []
    const flags = (device: SceneDevice): DeviceFlags => {
      const highlighted = highlightedNodes.has(device.id)
      return {
        selected: target === device.id || (device.kind === 'stack' && target === `project:${device.project?.key}`),
        highlighted: !playing && highlighted,
        dimmed: !playing && hasFocus && device.kind === 'service' && !highlighted && target !== device.id,
        flowHidden: playing && !flowRevealed.has(device.id),
        entering: entering.has(device.id),
        enterDelay: firstDraw.current ? device.stage * STAGE_DELAY_MS : 0,
      }
    }

    for (const plate of scene.plates) {
      const project = topology.projects.find((item) => item.key === plate.projectKey)
      if (!project && !plate.host) continue
      const xs = plate.corners.map((corner) => corner.x)
      const ys = plate.corners.map((corner) => corner.y)
      const origin = { x: Math.min(...xs), y: Math.min(...ys) }
      result.push({
        id: plate.id,
        type: 'plate',
        position: origin,
        width: Math.max(...xs) - origin.x,
        height: Math.max(...ys) - origin.y,
        draggable: false,
        connectable: false,
        selectable: false,
        zIndex: 0,
        data: {
          plate,
          project,
          origin,
          selected: target === plate.id,
          collapsed: collapsedSet.has(plate.projectKey),
          onToggle: () => toggleCollapsed(plate.projectKey),
        },
      })
    }

    for (const device of scene.devices) {
      const box = DEVICE_BOX[device.kind]
      const data: Record<string, unknown> = { ...flags(device) }
      if (device.kind === 'service') Object.assign(data, { service: device.service, now })
      if (device.kind === 'host') Object.assign(data, { server, now })
      if (device.kind === 'switch') Object.assign(data, { network: device.network })
      if (device.kind === 'hostService') Object.assign(data, { hostService: device.hostService })
      if (device.kind === 'stack') Object.assign(data, { project: device.project, now })
      result.push({
        id: device.id,
        type: device.kind,
        position: { x: device.point.x - box.anchorX, y: device.point.y - box.anchorY },
        width: box.width,
        height: box.height,
        draggable: false,
        connectable: false,
        zIndex: 2,
        data,
      })
    }

    // 재생 중에는 평소의 설명 카드를 띄우지 않는다. 재생 내용과 섞이면 안 된다.
    for (const activity of playing ? [] : mergeCallouts(shown).slice(0, MAX_CALLOUTS)) {
      const deviceID = visibleDevice(scene, activity.to)
      const device = scene.devices.find((item) => item.id === deviceID)
      if (!device) continue
      result.push({
        id: `callout:${activity.id}`,
        type: 'callout',
        position: { x: device.point.x + CALLOUT.offsetX, y: device.point.y - CALLOUT.offsetY },
        width: CALLOUT.width,
        height: CALLOUT.height,
        draggable: false,
        connectable: false,
        selectable: false,
        zIndex: 5,
        data: { activity },
      })
    }
    return result
    // toggleCollapsed는 collapsed에서 파생된다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scene, topology, server, target, now, highlightedNodes, hasFocus, collapsedSet, entering, shown, playing, flowRevealed])

  const edges = useMemo(() => {
    const result: Edge[] = []
    const focusIDs = new Set(highlightedNodes)
    if (target) focusIDs.add(target)
    const anyFocus = focusIDs.size > 0

    for (const link of scene.links) {
      const portLink = link.kind === 'external' || link.kind === 'publish' || link.kind === 'loopback'
      if (portLink && !layers.ports) continue
      if (link.kind === 'declared' && !layers.declared) continue
      if (link.kind === 'member' && !layers.networks && link.from !== target && link.to !== target) continue
      const lit = !playing && (focusIDs.has(link.from) || focusIDs.has(link.to))
      const isEntering = entering.has(link.id)
      const flowHiddenLink = playing && !(flowRevealed.has(link.from) && flowRevealed.has(link.to))
      result.push({
        id: link.id,
        type: 'iso',
        source: link.from,
        target: link.to,
        className: [`edge-${link.kind}`, lit && 'lit', !playing && anyFocus && !lit && 'faded', flowHiddenLink && 'flow-hidden'].filter(Boolean).join(' '),
        selectable: false,
        focusable: false,
        data: {
          d: link.d,
          label: link.label,
          labelAt: link.labelAt,
          marker: LINK_MARKER[link.kind],
          entering: isEntering,
          enterDelay: firstDraw.current ? link.stage * STAGE_DELAY_MS + 120 : 0,
        },
      })
    }

    // 재생 중에는 평소의 응답 확인 경로를 함께 그리지 않는다. 재생 내용과 섞이면 안 된다.
    const drawn = new Set<string>()
    for (const activity of playing ? [] : shown) {
      if (!activity.from || drawn.has(activity.id)) continue
      const d = routeBetween(scene, activity.from, activity.to)
      if (!d) continue
      drawn.add(activity.id)
      result.push({
        id: `activity:${activity.id}`,
        type: 'iso',
        source: activity.from,
        target: visibleDevice(scene, activity.to) ?? activity.to,
        className: `edge-activity ${activity.tone}`,
        selectable: false,
        focusable: false,
        zIndex: 4,
        data: { d, marker: activity.tone === 'fail' ? 'iso-arrow-fail' : 'iso-arrow-active', dot: true },
      })
    }

    if (currentPacket && frame && player) {
      const lastHop = Math.min(frame.hop, currentPacket.path.length - 2)
      for (let hop = 0; hop <= lastHop; hop++) {
        const route = hopRoute(scene, currentPacket.path[hop], currentPacket.path[hop + 1])
        if (!route) continue
        const moving = hop === lastHop
        result.push({
          id: moving ? `packet:${currentPacket.id}:${hop}` : `packet-done:${currentPacket.id}:${hop}`,
          type: moving ? 'packet' : 'iso',
          source: route.from,
          target: route.to,
          className: moving ? 'edge-packet' : `edge-packet-done packet-${currentPacket.kind}`,
          selectable: false,
          focusable: false,
          zIndex: 6,
          data: moving
            ? {
                d: route.d,
                reverse: route.reverse,
                kind: currentPacket.kind,
                title: currentPacket.title,
                detail: currentPacket.detail,
                protocol: currentPacket.protocol,
                example: player.script?.source === 'simulation',
                progress: player.progress,
              }
            : { d: route.d },
        })
      }
    }
    return result
  }, [scene, layers, target, highlightedNodes, entering, shown, playing, flowRevealed, currentPacket, frame, player])

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      nodesDraggable={false}
      nodesConnectable={false}
      edgesFocusable={false}
      minZoom={0.25}
      maxZoom={1.6}
      fitView
      fitViewOptions={{ padding: FIT_PADDING }}
      onMoveStart={(event) => {
        if (event) userMoved.current = true
      }}
      className={[zoom < FAR_ZOOM && 'lod-far', playing && 'flow-playing'].filter(Boolean).join(' ')}
      onNodeClick={(_, node) => {
        if (node.type === 'internet' || node.type === 'callout') return
        if (node.id === 'hostplate') {
          updateContext({ target: 'host', event: undefined })
        } else if (node.id.startsWith('stack:')) {
          const key = node.id.slice('stack:'.length)
          setProjectCollapsed(key, false)
          updateContext({ target: `project:${key}`, event: undefined })
        } else {
          updateContext({ target: node.id, event: undefined })
        }
      }}
      onPaneClick={() => updateContext({ target: undefined, event: undefined })}
    >
      <IsoGrid />
      <Panel position="top-left" className="map-tools">
        <button type="button" className="tool-button map-play-button" disabled={!onFlowPlay} title={onFlowPlay ? '통신 재생을 시작하거나 멈춥니다' : '재생할 경로가 없습니다'} onClick={onFlowPlay}>
          {flowPlayLabel}
        </button>
        <span className="tool-title">표시</span>
        <label>
          <input type="checkbox" checked={layers.ports} onChange={(e) => setLayers({ ...layers, ports: e.target.checked })} />
          공개 포트
        </label>
        <label>
          <input type="checkbox" checked={layers.networks} onChange={(e) => setLayers({ ...layers, networks: e.target.checked })} />
          네트워크 소속
        </label>
        <label>
          <input type="checkbox" checked={layers.declared} onChange={(e) => setLayers({ ...layers, declared: e.target.checked })} />
          선언 의존
        </label>
        <label title="응답 확인이 실행될 때 그 경로를 잠깐 그립니다">
          <input type="checkbox" checked={layers.activity} onChange={(e) => setLayers({ ...layers, activity: e.target.checked })} />
          응답 확인
        </label>
        <label className="disabled" title="요청 추적이 연동되지 않아 실제 호출 관계를 알 수 없습니다">
          <input type="checkbox" disabled />
          관측 요청 (미연동)
        </label>
        <button
          type="button"
          className="tool-button"
          onClick={() => {
            userMoved.current = false
            fitAll()
          }}
        >
          맞춤
        </button>
        <button type="button" className="tool-button" onClick={reset} title="비워진 자리를 정리합니다. 이때만 장비 위치가 바뀝니다">
          재정렬
        </button>
      </Panel>
      <Panel position="bottom-left" className="map-legend">
        <span><i className="line publish" />공개 포트</span>
        <span><i className="line loopback" />루프백 바인딩</span>
        <span><i className="line member" />네트워크 소속 (호출 아님)</span>
        <span><i className="line declared" />선언 의존 (호출 미확인)</span>
        <span><i className="line activity" />실제 응답 확인</span>
      </Panel>
      {player?.script && (
        <Panel position="top-right" className={`map-flow-tag ${player.script.source} ${server.mode === 'demo' ? 'demo' : ''}`} role="status">
          {player.script.source === 'simulation' ? '시뮬레이션: 실제 통신이 아닙니다' : server.mode === 'demo' ? '데모 기록: 가짜 데이터입니다' : '실제 응답 확인 기록'}
        </Panel>
      )}
      {highlight.note && !player && (
        <Panel position="top-right" className="map-note">
          {highlight.note}
        </Panel>
      )}
      <svg className="marker-defs" aria-hidden="true">
        <defs>
          {[
            ['iso-arrow', 'marker-main'],
            ['iso-arrow-muted', 'marker-muted'],
            ['iso-arrow-active', 'marker-active'],
            ['iso-arrow-fail', 'marker-fail'],
          ].map(([id, className]) => (
            <marker key={id} id={id} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="9" markerHeight="9" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
              <path d="M0 0L10 5L0 10z" className={className} />
            </marker>
          ))}
        </defs>
      </svg>
    </ReactFlow>
  )
}

export function TopologyMap(props: MapProps) {
  return (
    <ReactFlowProvider>
      <MapCanvas {...props} />
    </ReactFlowProvider>
  )
}
