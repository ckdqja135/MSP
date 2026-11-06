import { memo, useEffect, useMemo, useState } from 'react'
import {
  Handle,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { Project, Server, Service, Topology } from '../api'
import { updateContext } from '../context'
import { formatBytes, formatPercent, formatUptime, readMetric } from '../format'
import { assignSlots, computeLayout, emptySlots, type SlotMemory } from '../layout'
import { STATUS_LABEL, projectLabel, serviceHeadline } from '../status'
import { IsoContainer, IsoNetwork, IsoServer, StatusIcon, UnsupportedIcon } from './Icons'

export interface MapHighlight {
  nodes: ReadonlySet<string>
  note?: string
}

export interface Layers {
  ports: boolean
  networks: boolean
  declared: boolean
}

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

type ServiceData = { service: Service; selected: boolean; highlighted: boolean; dimmed: boolean; now: number }

const ServiceNode = memo(function ServiceNode({ data }: NodeProps<Node<ServiceData, 'service'>>) {
  const { service, selected, highlighted, dimmed, now } = data
  const published = service.containers.flatMap((c) => c.ports ?? []).filter((p) => p.host_port)
  const totals = serviceTotals(service)
  const cpu = readMetric(totals.cpu, now, formatPercent)
  const mem = readMetric(totals.mem, now, formatBytes)
  const stopped = service.runtime === 'stopped'
  const classes = ['service-card', service.status, selected && 'selected', highlighted && 'highlighted', dimmed && 'dimmed']
  return (
    <div className={classes.filter(Boolean).join(' ')}>
      <Handle type="target" position={Position.Left} id="left" className="hidden-handle" />
      <Handle type="source" position={Position.Right} id="right-out" className="hidden-handle" />
      <Handle type="target" position={Position.Right} id="right-in" className="hidden-handle" />
      <div className="card-head">
        <IsoContainer stopped={stopped} />
        <div className="card-title">
          <strong>{service.name}</strong>
          {service.containers.length > 1 && <span className="mono muted"> ×{service.containers.length}</span>}
        </div>
        <span className={`badge ${service.status}`}>
          <StatusIcon status={service.status} size={12} />
          {STATUS_LABEL[service.status]}
        </span>
      </div>
      <div className="card-line">{serviceHeadline(service)}</div>
      {published.length > 0 ? (
        <div className="card-line mono muted ellipsis">
          {published.map((p) => `${p.host_ip === '127.0.0.1' ? '127.0.0.1:' : ''}${p.host_port}→${p.container_port}`).join(' ')}
        </div>
      ) : (
        <div className="card-line muted">공개 포트 없음</div>
      )}
      {stopped ? (
        <div className="card-line muted">지표 없음 (실행 중 아님)</div>
      ) : (
        <div className="card-line mono">
          <span className={cpu.state === 'stale' ? 'stale' : ''}>CPU {cpu.text}</span>
          <span className={mem.state === 'stale' ? 'stale' : ''}> · MEM {mem.text}</span>
        </div>
      )}
    </div>
  )
})

type HostData = { server: Server; selected: boolean; now: number }

const HostNode = memo(function HostNode({ data }: NodeProps<Node<HostData, 'host'>>) {
  const { server, selected, now } = data
  const metric = (name: string, format: (value: number) => string) => readMetric(server.metrics[name], now, format)
  const temperature = server.collectors.find((c) => c.name === 'temperature')
  return (
    <div className={`host-card ${selected ? 'selected' : ''}`}>
      <Handle type="source" position={Position.Bottom} className="hidden-handle" />
      <IsoServer />
      <div className="host-name">
        <strong>{server.id}</strong>
        <span className="muted">서버 · 가동 {formatUptime(server.uptime_seconds)}</span>
      </div>
      <div className="host-metrics mono">
        <span>CPU {metric('host.cpu.percent', formatPercent).text}</span>
        <span>MEM {metric('host.mem.percent', formatPercent).text}</span>
        <span>DISK {metric('host.disk.percent', formatPercent).text}</span>
        {temperature?.state === 'unsupported' ? (
          <span className="muted">
            <UnsupportedIcon /> 온도 미지원
          </span>
        ) : (
          <span>온도 {metric('host.temperature', (v) => `${v.toFixed(0)}°C`).text}</span>
        )}
      </div>
    </div>
  )
})

type GroupData = { project: Project; selected: boolean; collapsed: boolean; onToggle: () => void }

const GroupNode = memo(function GroupNode({ data }: NodeProps<Node<GroupData, 'project'>>) {
  const { project, selected, collapsed, onToggle } = data
  return (
    <div className={`project-group ${selected ? 'selected' : ''}`}>
      <div className="group-head">
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
        <span className={`badge ${project.status}`}>
          <StatusIcon status={project.status} size={12} />
          {STATUS_LABEL[project.status]}
        </span>
      </div>
    </div>
  )
})

type NetworkData = { name: string; subnet?: string; count: number; selected: boolean; rail: boolean }

const NetworkNode = memo(function NetworkNode({ data }: NodeProps<Node<NetworkData, 'network'>>) {
  return (
    <div className={`network-node ${data.rail ? 'rail' : 'band'} ${data.selected ? 'selected' : ''}`}>
      <Handle type="source" position={Position.Bottom} className="hidden-handle" />
      <IsoNetwork size={14} />
      <span className="mono ellipsis">{data.name}</span>
      {data.rail && data.subnet && <span className="mono muted ellipsis">{data.subnet}</span>}
      <span className="muted network-note">{data.rail ? `소속 ${data.count}개, 호출 관계 아님` : `소속 ${data.count}개`}</span>
    </div>
  )
})

const nodeTypes = { service: ServiceNode, host: HostNode, project: GroupNode, network: NetworkNode }

function FitButton() {
  const { fitView } = useReactFlow()
  return (
    <button type="button" className="tool-button" onClick={() => fitView({ padding: 0.06, duration: 150 })}>
      맞춤
    </button>
  )
}

function FocusOnTarget({ nodeID, enabled }: { nodeID?: string; enabled: boolean }) {
  const { fitView } = useReactFlow()
  useEffect(() => {
    if (enabled && nodeID) fitView({ nodes: [{ id: nodeID }], maxZoom: 1, padding: 1.2, duration: 200 })
  }, [enabled, nodeID, fitView])
  return null
}

interface MapProps {
  server: Server
  topology: Topology
  target?: string
  highlight: MapHighlight
  now: number
  focusNode?: string
}

function MapCanvas({ server, topology, target, highlight, now, focusNode }: MapProps) {
  const slotKey = `msp.slots.${server.mode}.${server.id}`
  const collapseKey = `msp.collapsed.${server.mode}.${server.id}`
  const [slots, setSlots] = useState<SlotMemory>(() => loadJSON(slotKey, emptySlots()))
  const [collapsed, setCollapsed] = useState<string[]>(() => loadJSON(collapseKey, []))
  const [layers, setLayers] = useState<Layers>({ ports: true, networks: false, declared: false })

  const currentSlots = useMemo(() => assignSlots(slots, topology), [slots, topology])
  useEffect(() => {
    if (JSON.stringify(currentSlots) !== JSON.stringify(slots)) {
      setSlots(currentSlots)
      window.localStorage.setItem(slotKey, JSON.stringify(currentSlots))
    }
  }, [currentSlots, slots, slotKey])

  const collapsedSet = useMemo(() => new Set(collapsed), [collapsed])
  const layout = useMemo(() => computeLayout(topology, currentSlots, collapsedSet), [topology, currentSlots, collapsedSet])

  const toggleCollapsed = (key: string) => {
    const next = collapsedSet.has(key) ? collapsed.filter((item) => item !== key) : [...collapsed, key]
    setCollapsed(next)
    window.localStorage.setItem(collapseKey, JSON.stringify(next))
  }

  const reset = () => {
    const fresh = assignSlots(emptySlots(), topology)
    setSlots(fresh)
    window.localStorage.setItem(slotKey, JSON.stringify(fresh))
  }

  const selectedNetwork = target?.startsWith('net:') ? target.slice(4) : undefined
  // 네트워크를 선택하면 소속 서비스를 강조한다. 소속만 뜻하고 호출 관계는 뜻하지 않는다.
  const highlightedNodes = useMemo(() => {
    const members = topology.networks.find((network) => network.name === selectedNetwork)?.members ?? []
    return new Set([...highlight.nodes, ...members.map((member) => `svc:${member}`)])
  }, [highlight.nodes, selectedNetwork, topology.networks])
  const hasHighlight = highlightedNodes.size > 0

  const nodes = useMemo(() => {
    const result: Node[] = []
    const place = (id: string, type: string, box: { x: number; y: number; width: number; height: number }, data: Record<string, unknown>, zIndex: number) => {
      result.push({
        id,
        type,
        data,
        position: { x: box.x, y: box.y },
        width: box.width,
        height: box.height,
        draggable: false,
        connectable: false,
        zIndex,
      })
    }
    place('host', 'host', layout.host, { server, selected: target === 'host', now }, 2)

    for (const network of topology.networks) {
      const id = `net:${network.name}`
      const data = { name: network.name, subnet: network.subnet, count: network.members.length, selected: target === id }
      if (layout.rails[network.name]) place(id, 'network', layout.rails[network.name], { ...data, rail: true }, 1)
      else if (layout.bands[network.name]) place(id, 'network', layout.bands[network.name], { ...data, rail: false }, 2)
    }

    for (const project of topology.projects) {
      const box = layout.groups[project.key]
      if (!box) continue
      const id = `project:${project.key}`
      place(id, 'project', box, {
        project,
        selected: target === id,
        collapsed: collapsedSet.has(project.key),
        onToggle: () => toggleCollapsed(project.key),
      }, 0)
      for (const service of project.services) {
        const serviceBox = layout.services[service.key]
        if (!serviceBox) continue
        const serviceID = `svc:${service.key}`
        const highlighted = highlightedNodes.has(serviceID)
        place(serviceID, 'service', serviceBox, {
          service,
          now,
          selected: target === serviceID,
          highlighted,
          dimmed: hasHighlight && !highlighted && target !== serviceID,
        }, 2)
      }
    }
    return result
    // toggleCollapsed는 collapsed에서 파생된다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topology, layout, server, target, now, highlightedNodes, hasHighlight, collapsedSet])

  const edges = useMemo(() => {
    const result: Edge[] = []
    const visible = (key: string) => Boolean(layout.services[key])
    for (const relation of topology.relations) {
      if (relation.type === 'publishes' && layers.ports && visible(relation.to)) {
        const loopback = relation.detail?.startsWith('127.') || relation.detail?.startsWith('::1')
        result.push({
          id: `publish:${relation.to}:${relation.detail}`,
          source: 'host',
          target: `svc:${relation.to}`,
          targetHandle: 'left',
          type: 'smoothstep',
          className: loopback ? 'edge-publish-loopback' : 'edge-publish',
          selectable: false,
        })
      }
      if (relation.type === 'declared_depends_on' && layers.declared && visible(relation.from) && visible(relation.to)) {
        result.push({
          id: `declared:${relation.from}:${relation.to}`,
          source: `svc:${relation.from}`,
          sourceHandle: 'right-out',
          target: `svc:${relation.to}`,
          targetHandle: 'right-in',
          type: 'smoothstep',
          className: 'edge-declared',
          label: '선언',
          selectable: false,
          markerEnd: 'declared-arrow',
        })
      }
      if (relation.type === 'member_of' && visible(relation.from)) {
        const network = relation.to.slice('network:'.length)
        if ((layers.networks || selectedNetwork === network) && layout.rails[network]) {
          result.push({
            id: `member:${relation.from}:${network}`,
            source: relation.to.replace('network:', 'net:'),
            target: `svc:${relation.from}`,
            targetHandle: 'left',
            type: 'smoothstep',
            className: 'edge-member',
            selectable: false,
          })
        }
      }
    }
    return result
  }, [topology, layout, layers, selectedNetwork])

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      nodesDraggable={false}
      nodesConnectable={false}
      edgesFocusable={false}
      minZoom={0.25}
      maxZoom={1.6}
      fitView
      fitViewOptions={{ padding: 0.06 }}
      onNodeClick={(_, node) => updateContext({ target: node.id, event: undefined })}
      onPaneClick={() => updateContext({ target: undefined, event: undefined })}
    >
      <FocusOnTarget nodeID={focusNode} enabled={Boolean(focusNode)} />
      <Panel position="top-left" className="map-tools">
        <span className="tool-title">레이어</span>
        <label>
          <input type="checkbox" checked={layers.ports} onChange={(e) => setLayers({ ...layers, ports: e.target.checked })} />
          공개 포트
        </label>
        <label>
          <input type="checkbox" checked={layers.networks} onChange={(e) => setLayers({ ...layers, networks: e.target.checked })} />
          네트워크 소속선
        </label>
        <label>
          <input type="checkbox" checked={layers.declared} onChange={(e) => setLayers({ ...layers, declared: e.target.checked })} />
          선언 의존
        </label>
        <label className="disabled" title="요청 추적이 연동되지 않아 실제 호출 관계를 알 수 없습니다">
          <input type="checkbox" disabled />
          관측 요청 (미연동)
        </label>
        <FitButton />
        <button type="button" className="tool-button" onClick={reset} title="새 항목으로 비워진 자리를 정리합니다. 이때만 노드 위치가 바뀝니다">
          재정렬
        </button>
      </Panel>
      <Panel position="bottom-left" className="map-legend">
        <span><i className="line publish" />공개 포트 바인딩</span>
        <span><i className="line loopback" />루프백 바인딩</span>
        <span><i className="line member" />네트워크 소속 (호출 아님)</span>
        <span><i className="line declared" />선언 의존 (호출 미확인)</span>
      </Panel>
      {highlight.note && (
        <Panel position="top-right" className="map-note">
          {highlight.note}
        </Panel>
      )}
      <svg className="marker-defs" aria-hidden="true">
        <defs>
          <marker id="declared-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0L10 5L0 10z" className="marker-declared" />
          </marker>
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
