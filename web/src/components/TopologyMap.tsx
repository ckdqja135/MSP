import { memo, useEffect, useMemo, useRef, useState } from 'react'
import {
  Handle,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  useViewport,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { diffActivities, eventActivity, mergeCallouts, type Activity } from '../activity'
import type { EventRow, Network, Project, Server, Service, Topology } from '../api'
import { updateContext } from '../context'
import { compactPorts, formatBytes, formatPercent, formatUptime, readMetric } from '../format'
import {
  CELL_X,
  CELL_Y,
  assignSlots,
  computeScene,
  emptySlots,
  routeBetween,
  serviceRole,
  type Point,
  type SceneDevice,
  type ScenePlate,
  type SlotMemory,
} from '../scene'
import { STATUS_LABEL, projectLabel, serviceHeadline } from '../status'
import { StatusIcon, UnsupportedIcon } from './Icons'
import { CloudIcon, HostIcon, ServiceIcon, SwitchIcon } from './IsoIcons'

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
  entering: boolean
  enterDelay: number
}

function deviceClasses(base: string, flags: DeviceFlags, extra?: string): string {
  return [base, extra, flags.selected && 'selected', flags.highlighted && 'highlighted', flags.dimmed && 'dimmed', flags.entering && 'entering']
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

type PlateData = { plate: ScenePlate; project: Project; selected: boolean; collapsed: boolean; onToggle: () => void; origin: Point }

// 프로젝트 판은 시각적 묶음이다. 머리글만 클릭을 받고 나머지는 뒤의 장비와 화면 이동을 막지 않는다.
const PlateNode = memo(function PlateNode({ data }: NodeProps<Node<PlateData, 'plate'>>) {
  const { plate, project, selected, collapsed, onToggle, origin } = data
  const points = plate.corners.map((corner) => `${corner.x - origin.x},${corner.y - origin.y}`).join(' ')
  // 머리글은 판의 위 꼭짓점 위에 둔다. 옆 프로젝트의 라벨과 겹치지 않는 자리다.
  const apex = plate.corners[1]
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

const edgeTypes = { iso: IsoEdge }

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
}

function MapCanvas({ server, topology, target, highlight, now, live, selectedEvent, focus }: MapProps) {
  const { fitView } = useReactFlow()
  const slotKey = `msp.slots.${server.mode}.${server.id}`
  const collapseKey = `msp.collapsed.${server.mode}.${server.id}`
  const [slots, setSlots] = useState<SlotMemory>(() => loadJSON(slotKey, emptySlots()))
  const [collapsed, setCollapsed] = useState<string[]>(() => loadJSON(collapseKey, []))
  const [layers, setLayers] = useState<Layers>({ ports: true, networks: true, declared: false, activity: true })

  const currentSlots = useMemo(() => assignSlots(slots, topology), [slots, topology])
  useEffect(() => {
    if (JSON.stringify(currentSlots) !== JSON.stringify(slots)) {
      setSlots(currentSlots)
      window.localStorage.setItem(slotKey, JSON.stringify(currentSlots))
    }
  }, [currentSlots, slots, slotKey])

  const collapsedSet = useMemo(() => new Set(collapsed), [collapsed])
  const scene = useMemo(() => computeScene(topology, currentSlots, collapsedSet), [topology, currentSlots, collapsedSet])

  const toggleCollapsed = (key: string) => {
    const next = collapsedSet.has(key) ? collapsed.filter((item) => item !== key) : [...collapsed, key]
    setCollapsed(next)
    window.localStorage.setItem(collapseKey, JSON.stringify(next))
  }

  const userMoved = useRef(false)
  const fitAll = () => fitView({ padding: FIT_PADDING, duration: 450 })

  const reset = () => {
    const fresh = assignSlots(emptySlots(), topology)
    setSlots(fresh)
    window.localStorage.setItem(slotKey, JSON.stringify(fresh))
    userMoved.current = false
    window.setTimeout(fitAll, 60)
  }

  // 알림에서 들어오면 대상 장비로 화면을 옮긴다.
  useEffect(() => {
    if (!focus || !target) return
    userMoved.current = true
    const timer = window.setTimeout(() => fitView({ nodes: [{ id: target }], maxZoom: 1.1, padding: 1.1, duration: 450 }), 300)
    return () => window.clearTimeout(timer)
  }, [focus, target, fitView])

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
        selected: target === device.id,
        highlighted,
        dimmed: hasFocus && device.kind === 'service' && !highlighted && target !== device.id,
        entering: entering.has(device.id),
        enterDelay: firstDraw.current ? device.stage * STAGE_DELAY_MS : 0,
      }
    }

    for (const plate of scene.plates) {
      const project = topology.projects.find((item) => item.key === plate.projectKey)
      if (!project) continue
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

    for (const activity of mergeCallouts(shown).slice(0, MAX_CALLOUTS)) {
      const device = scene.devices.find((item) => item.id === activity.to)
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
  }, [scene, topology, server, target, now, highlightedNodes, hasFocus, collapsedSet, entering, shown])

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
      const lit = focusIDs.has(link.from) || focusIDs.has(link.to)
      const isEntering = entering.has(link.id)
      result.push({
        id: link.id,
        type: 'iso',
        source: link.from,
        target: link.to,
        className: [`edge-${link.kind}`, lit && 'lit', anyFocus && !lit && 'faded'].filter(Boolean).join(' '),
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

    const drawn = new Set<string>()
    for (const activity of shown) {
      if (!activity.from || drawn.has(activity.id)) continue
      const d = routeBetween(scene, activity.from, activity.to)
      if (!d) continue
      drawn.add(activity.id)
      result.push({
        id: `activity:${activity.id}`,
        type: 'iso',
        source: activity.from,
        target: activity.to,
        className: `edge-activity ${activity.tone}`,
        selectable: false,
        focusable: false,
        zIndex: 4,
        data: { d, marker: activity.tone === 'fail' ? 'iso-arrow-fail' : 'iso-arrow-active', dot: true },
      })
    }
    return result
  }, [scene, layers, target, highlightedNodes, entering, shown])

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
      onNodeClick={(_, node) => {
        if (node.type !== 'internet' && node.type !== 'callout') updateContext({ target: node.id, event: undefined })
      }}
      onPaneClick={() => updateContext({ target: undefined, event: undefined })}
    >
      <IsoGrid />
      <Panel position="top-left" className="map-tools">
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
      {highlight.note && (
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
