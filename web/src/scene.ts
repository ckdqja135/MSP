import type { HostService, Network, PortBinding, Project, Service, Topology } from './api'
import { isLoopbackAddress, summarizePorts } from './format'
import { hostServiceKey } from './status'

// 구조도를 등각 격자 위에 배치한다. a는 오른쪽 아래, b는 오른쪽 위로 가는 축이다.
export const CELL_X = 118
export const CELL_Y = 68

export interface Point {
  x: number
  y: number
}

export const iso = (a: number, b: number): Point => ({ x: (a + b) * CELL_X, y: (a - b) * CELL_Y })

const COLUMN_GAP = 1.5
const ROW_GAP = 1.16
const RAIL_START_B = -1.5
const FIRST_RAIL_A = -1
const RAIL_GAP_A = 0.42
const HOST_AT = { a: -1.75, b: -2.3 }
const INTERNET_AT = { a: -0.2, b: -3 }
const HOST_COLUMN_B = -1.5
// 서비스가 이보다 많으면 문제없는 프로젝트를 한 장비로 접어 전체가 읽히게 한다.
const AUTO_COLLAPSE_SERVICES = 12

// 연결선이 지나가는 통로다. 장비 위로 지나가지 않도록 열 사이로 돌린다.
const LOCAL_BUS = -0.34
const SHARED_LANE = 0.36
const PUBLISH_LANE = 0.2
const DECLARED_LANE = 0.5

const PLATE_HEAD = 0.48
const PLATE_TAIL = 0.62
const PLATE_SIDE = 0.6

// SlotMemory는 프로젝트 열과 서비스 칸의 배정 기록이다.
// 한 번 배정한 자리는 재정렬 전까지 바꾸지 않아 갱신 때 장비가 움직이지 않는다.
export interface SlotMemory {
  projects: string[]
  services: Record<string, string[]>
  hosts?: string[]
}

export const emptySlots = (): SlotMemory => ({ projects: [], services: {}, hosts: [] })

// collapsedProjects는 접힌 프로젝트를 정한다. 사용자가 직접 바꾼 것을 먼저 따르고,
// 자동으로 접을 때도 장애나 주의가 있는 프로젝트는 펼쳐 둔다.
export function collapsedProjects(topology: Topology, overrides: Readonly<Record<string, boolean>>): Set<string> {
  const total = topology.projects.flatMap((project) => project.services).filter((service) => !isFinishedJob(service)).length
  const crowded = total > AUTO_COLLAPSE_SERVICES
  const collapsed = new Set<string>()
  for (const project of topology.projects) {
    const quiet = project.status === 'ok' || project.status === 'stopped'
    if (overrides[project.key] ?? (crowded && quiet)) collapsed.add(project.key)
  }
  return collapsed
}

const STANDALONE = '_standalone'

function firstAssignmentOrder(a: string, b: string): number {
  if (a === STANDALONE) return 1
  if (b === STANDALONE) return -1
  return a.localeCompare(b)
}

// 끝난 일회성 작업은 구조도에 그리지 않는다. 왼쪽 목록과 상세에서는 볼 수 있다.
export function isFinishedJob(service: Service): boolean {
  return service.containers.length > 0 && service.containers.every((c) => c.one_off && c.state !== 'running')
}

// assignSlots는 처음 보는 항목만 뒤에 추가한다. 사라진 항목의 자리는 비워 둔다.
export function assignSlots(previous: SlotMemory, topology: Topology): SlotMemory {
  const next: SlotMemory = {
    projects: [...previous.projects],
    services: Object.fromEntries(Object.entries(previous.services).map(([key, list]) => [key, [...list]])),
    hosts: [...(previous.hosts ?? [])],
  }
  for (const service of topology.host_services ?? []) {
    const key = hostServiceKey(service)
    if (!next.hosts!.includes(key)) next.hosts!.push(key)
  }
  const newProjects = topology.projects
    .map((project) => project.key)
    .filter((key) => !next.projects.includes(key))
    .sort(firstAssignmentOrder)
  next.projects.push(...newProjects)
  for (const project of topology.projects) {
    const slots = (next.services[project.key] ??= [])
    for (const service of project.services) {
      if (!isFinishedJob(service) && !slots.includes(service.key)) slots.push(service.key)
    }
  }
  return next
}

export type DeviceKind = 'internet' | 'host' | 'switch' | 'service' | 'stack' | 'hostService'

export interface SceneDevice {
  id: string
  kind: DeviceKind
  point: Point
  // 처음 그릴 때 바깥쪽 장비부터 차례로 나타나게 하는 순서다.
  stage: number
  service?: Service
  network?: Network
  project?: Project
  hostService?: HostService
}

export interface ScenePlate {
  id: string
  projectKey: string
  corners: [Point, Point, Point, Point]
  hiddenJobs: number
  // Docker 밖 서비스를 묶는 판이다. 프로젝트처럼 접지 않는다.
  host?: boolean
}

export type LinkKind = 'external' | 'publish' | 'loopback' | 'member' | 'declared'

export interface SceneLink {
  id: string
  kind: LinkKind
  from: string
  to: string
  d: string
  label?: string
  labelAt?: Point
  stage: number
}

interface Lattice {
  a: number
  b: number
}

export interface Scene {
  devices: SceneDevice[]
  plates: ScenePlate[]
  links: SceneLink[]
  positions: Record<string, Lattice>
}

function pathOf(points: Lattice[]): string {
  return points
    .map((point, index) => {
      const { x, y } = iso(point.a, point.b)
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
}

// laneRoute는 축을 따라 꺾이는 경로를 만든다. 같은 장비에서 나온 선은 앞부분이 겹쳐 점선 간격이 맞는다.
function laneRoute(from: Lattice, to: Lattice, lane: number): string {
  const b = to.b + lane
  return pathOf([from, { a: from.a, b }, { a: to.a, b }, to])
}

const rowA = (row: number) => 1 + row * ROW_GAP

const isLoopback = isLoopbackAddress

// computeScene은 구조 데이터를 그림으로 바꾼다. 네트워크 소속은 네트워크 장비로만 연결하고
// 서비스끼리 잇는 선은 선언된 의존에서만 만든다.
export function computeScene(topology: Topology, slots: SlotMemory, collapsed: ReadonlySet<string>): Scene {
  const devices: SceneDevice[] = []
  const plates: ScenePlate[] = []
  const links: SceneLink[] = []
  const positions: Record<string, Lattice> = {}

  const place = (id: string, kind: DeviceKind, at: Lattice, stage: number, extra: Partial<SceneDevice> = {}) => {
    positions[id] = at
    devices.push({ id, kind, point: iso(at.a, at.b), stage, ...extra })
  }

  const services = new Map<string, Service>()
  for (const project of topology.projects) {
    for (const service of project.services) services.set(service.key, service)
  }
  const present = new Set(topology.projects.map((project) => project.key))
  const allServices = [...services.values()]
  const publicPorts = allServices.flatMap((service) =>
    service.containers.flatMap((container) => (container.ports ?? []).filter((port) => port.host_port && !isLoopback(port.host_ip))),
  )
  const hasExternalProbe = allServices.some((service) => service.probes.some((probe) => probe.vantage === 'external'))
  const hostServices = topology.host_services ?? []
  const publicHostPorts: PortBinding[] = hostServices.flatMap((service) =>
    service.listening
      ? service.ports.filter((port) => !isLoopback(port.ip)).map((port) => ({ host_ip: port.ip, host_port: port.port, container_port: port.port, proto: 'tcp' }))
      : [],
  )

  place('host', 'host', HOST_AT, 1)
  if (publicPorts.length + publicHostPorts.length > 0 || hasExternalProbe) {
    place('internet', 'internet', INTERNET_AT, 0)
    const ports = summarizePorts([...publicPorts, ...publicHostPorts], 3)
    links.push({
      id: 'external',
      kind: 'external',
      from: 'internet',
      to: 'host',
      d: pathOf([INTERNET_AT, { a: INTERNET_AT.a, b: HOST_AT.b }, HOST_AT]),
      label: ports ? `포트 ${ports}` : undefined,
      labelAt: iso((INTERNET_AT.a + HOST_AT.a) / 2, HOST_AT.b),
      stage: 1,
    })
  }

  // Docker 밖의 서비스는 서버 옆 열에 둔다. 컨테이너와 구분해 볼 수 있게 하기 위해서다.
  const hostSlots = slots.hosts ?? []
  const shownHosts = hostSlots.filter((key) => hostServices.some((service) => hostServiceKey(service) === key))
  if (shownHosts.length > 0) {
    const lastRow = rowA(hostSlots.length - 1)
    plates.push({
      id: 'hostplate',
      projectKey: '',
      host: true,
      hiddenJobs: 0,
      corners: [
        iso(-PLATE_HEAD, HOST_COLUMN_B - PLATE_SIDE),
        iso(-PLATE_HEAD, HOST_COLUMN_B + PLATE_SIDE),
        iso(lastRow + PLATE_TAIL, HOST_COLUMN_B + PLATE_SIDE),
        iso(lastRow + PLATE_TAIL, HOST_COLUMN_B - PLATE_SIDE),
      ],
    })
    hostSlots.forEach((key, row) => {
      const hostService = hostServices.find((service) => hostServiceKey(service) === key)
      if (!hostService) return
      const id = `hostsvc:${key}`
      const at = { a: rowA(row), b: HOST_COLUMN_B }
      place(id, 'hostService', at, 3, { hostService })
      const open = hostService.listening && hostService.ports.some((port) => !isLoopback(port.ip))
      links.push({ id: `hostlink:${key}`, kind: open ? 'publish' : 'loopback', from: 'host', to: id, d: laneRoute(HOST_AT, at, LOCAL_BUS), stage: 3 })
    })
  }

  const columnOf = new Map(slots.projects.map((key, index) => [key, index * COLUMN_GAP]))

  slots.projects.forEach((projectKey, column) => {
    if (!present.has(projectKey)) return
    const b = column * COLUMN_GAP
    const isCollapsed = collapsed.has(projectKey)
    const slotted = slots.services[projectKey] ?? []
    const rows = isCollapsed ? 1 : slotted.length
    const lastA = rows > 0 ? rowA(rows - 1) : 0
    const project = topology.projects.find((item) => item.key === projectKey)!
    plates.push({
      id: `project:${projectKey}`,
      projectKey,
      corners: [
        iso(-PLATE_HEAD, b - PLATE_SIDE),
        iso(-PLATE_HEAD, b + PLATE_SIDE),
        iso(lastA + PLATE_TAIL, b + PLATE_SIDE),
        iso(lastA + PLATE_TAIL, b - PLATE_SIDE),
      ],
      hiddenJobs: project.services.filter(isFinishedJob).length,
    })
    if (isCollapsed) {
      place(`stack:${projectKey}`, 'stack', { a: rowA(0), b }, 3, { project })
      return
    }
    slotted.forEach((serviceKey, row) => {
      const service = services.get(serviceKey)
      if (service && !isFinishedJob(service)) place(`svc:${serviceKey}`, 'service', { a: rowA(row), b }, 3, { service })
    })
  })

  // 한 프로젝트 안에서만 쓰는 네트워크는 프로젝트 맨 앞의 스위치로 그린다.
  const localIndex = new Map<string, number>()
  const shared = topology.networks.filter((network) => network.shared)
  shared.forEach((network, index) => {
    const a = FIRST_RAIL_A - index * RAIL_GAP_A
    place(`net:${network.name}`, 'switch', { a, b: RAIL_START_B }, 2, { network })
  })
  for (const network of topology.networks) {
    if (network.shared) continue
    const projectKey = network.members[0]?.split('/')[0]
    const b = columnOf.get(projectKey)
    if (b === undefined || collapsed.has(projectKey) || !present.has(projectKey)) continue
    const order = localIndex.get(projectKey) ?? 0
    localIndex.set(projectKey, order + 1)
    const rows = (slots.services[projectKey] ?? []).length
    place(`net:${network.name}`, 'switch', { a: order === 0 ? 0 : rowA(rows - 1 + order), b }, 2, { network })
  }

  const visible = (id: string) => positions[id] !== undefined
  // 접힌 프로젝트의 서비스는 프로젝트 장비로 연결한다.
  const deviceOf = (serviceKey: string): string | undefined => {
    if (visible(`svc:${serviceKey}`)) return `svc:${serviceKey}`
    const stack = `stack:${serviceKey.split('/')[0]}`
    return visible(stack) ? stack : undefined
  }

  for (const network of topology.networks) {
    const switchID = `net:${network.name}`
    if (!visible(switchID)) continue
    const from = positions[switchID]
    const linked = new Set<string>()
    for (const member of network.members) {
      const device = deviceOf(member)
      if (!device || linked.has(device)) continue
      linked.add(device)
      links.push({
        id: `member:${network.name}:${device}`,
        kind: 'member',
        from: switchID,
        to: device,
        d: laneRoute(from, positions[device], network.shared ? SHARED_LANE : LOCAL_BUS),
        stage: 3,
      })
    }
    if (network.shared) {
      links.push({ id: `host-net:${network.name}`, kind: 'member', from: 'host', to: switchID, d: pathOf([HOST_AT, { a: from.a, b: HOST_AT.b }, from]), stage: 2 })
    }
  }

  const portLinks = new Set<string>()
  for (const service of allServices) {
    const id = deviceOf(service.key)
    if (!id) continue
    const to = positions[id]
    const published = service.containers.flatMap((container) => (container.ports ?? []).filter((port) => port.host_port))
    const reachable = published.filter((port) => !isLoopback(port.host_ip))
    const loopback = published.filter((port) => isLoopback(port.host_ip))
    if (reachable.length > 0 && !portLinks.has(`publish:${id}`)) {
      portLinks.add(`publish:${id}`)
      links.push({ id: `publish:${id}`, kind: 'publish', from: 'host', to: id, d: laneRoute(HOST_AT, to, PUBLISH_LANE), stage: 3 })
    }
    if (loopback.length > 0 && !portLinks.has(`loopback:${id}`)) {
      portLinks.add(`loopback:${id}`)
      links.push({ id: `loopback:${id}`, kind: 'loopback', from: 'host', to: id, d: laneRoute(HOST_AT, to, PUBLISH_LANE), stage: 3 })
    }
  }

  for (const relation of topology.relations) {
    if (relation.type !== 'declared_depends_on') continue
    const from = positions[`svc:${relation.from}`]
    const to = positions[`svc:${relation.to}`]
    if (!from || !to) continue
    links.push({
      id: `declared:${relation.from}:${relation.to}`,
      kind: 'declared',
      from: `svc:${relation.from}`,
      to: `svc:${relation.to}`,
      d: laneRoute(from, to, DECLARED_LANE),
      stage: 3,
    })
  }

  return { devices, plates, links, positions }
}

// visibleDevice는 대상이 접혀 있으면 그 프로젝트 장비를 돌려준다.
export function visibleDevice(scene: Scene, id: string): string | undefined {
  if (scene.positions[id]) return id
  if (!id.startsWith('svc:')) return undefined
  const stack = `stack:${id.slice(4).split('/')[0]}`
  return scene.positions[stack] ? stack : undefined
}

// routeBetween은 실제로 일어난 연결을 그릴 경로를 만든다.
export function routeBetween(scene: Scene, from: string, to: string): string | undefined {
  const start = scene.positions[from]
  const device = visibleDevice(scene, to)
  const end = device ? scene.positions[device] : undefined
  if (!start || !end) return undefined
  return laneRoute(start, end, PUBLISH_LANE)
}

export type ServiceRole = 'database' | 'cache' | 'proxy' | 'storage' | 'web' | 'app'

// serviceRole은 아이콘 모양을 고르기 위한 추정이다. 이미지와 서비스 이름만 보며 다른 판단에는 쓰지 않는다.
export function serviceRole(service: Service): ServiceRole {
  return roleFromText(service.containers[0]?.image.toLowerCase() ?? '', service.name)
}

export function hostServiceRole(service: HostService): ServiceRole {
  return roleFromText(service.name.toLowerCase(), service.name)
}

function roleFromText(image: string, name: string): ServiceRole {
  if (/postgres|mysql|mariadb|mongo|cockroach|timescale|clickhouse/.test(image)) return 'database'
  if (/redis|valkey|memcached|keydb/.test(image)) return 'cache'
  if (/nginx|caddy|traefik|haproxy|envoy|apache|httpd/.test(image)) return 'proxy'
  if (/minio|seaweed|garage|ceph/.test(image)) return 'storage'
  if (/\b(web|front|ui|site)\b/.test(`${name} ${image}`.replace(/[-_/:]/g, ' '))) return 'web'
  return 'app'
}
