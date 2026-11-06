import type { Network, Service, Topology } from './api'
import { summarizePorts } from './format'

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
const RAIL_START_B = -1
const FIRST_RAIL_A = -0.7
const RAIL_GAP_A = 0.42
const HOST_AT = { a: -1.05, b: -2.05 }
const INTERNET_AT = { a: 0.75, b: -2.05 }

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
}

export const emptySlots = (): SlotMemory => ({ projects: [], services: {} })

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

export type DeviceKind = 'internet' | 'host' | 'switch' | 'service'

export interface SceneDevice {
  id: string
  kind: DeviceKind
  point: Point
  // 처음 그릴 때 바깥쪽 장비부터 차례로 나타나게 하는 순서다.
  stage: number
  service?: Service
  network?: Network
}

export interface ScenePlate {
  id: string
  projectKey: string
  corners: [Point, Point, Point, Point]
  hiddenJobs: number
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

function isLoopback(host?: string): boolean {
  return host === '127.0.0.1' || host === '::1'
}

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

  place('host', 'host', HOST_AT, 1)
  if (publicPorts.length > 0 || hasExternalProbe) {
    place('internet', 'internet', INTERNET_AT, 0)
    const ports = summarizePorts(publicPorts, 3)
    links.push({
      id: 'external',
      kind: 'external',
      from: 'internet',
      to: 'host',
      d: pathOf([INTERNET_AT, HOST_AT]),
      label: ports ? `포트 ${ports}` : undefined,
      labelAt: iso((INTERNET_AT.a + HOST_AT.a) / 2, HOST_AT.b),
      stage: 1,
    })
  }

  const columnOf = new Map(slots.projects.map((key, index) => [key, index * COLUMN_GAP]))

  slots.projects.forEach((projectKey, column) => {
    if (!present.has(projectKey)) return
    const b = column * COLUMN_GAP
    const isCollapsed = collapsed.has(projectKey)
    const slotted = slots.services[projectKey] ?? []
    const rows = isCollapsed ? 0 : slotted.length
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
    if (isCollapsed) return
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

  for (const network of topology.networks) {
    const switchID = `net:${network.name}`
    if (!visible(switchID)) continue
    const from = positions[switchID]
    for (const member of network.members) {
      const to = positions[`svc:${member}`]
      if (!to) continue
      links.push({
        id: `member:${network.name}:${member}`,
        kind: 'member',
        from: switchID,
        to: `svc:${member}`,
        d: laneRoute(from, to, network.shared ? SHARED_LANE : LOCAL_BUS),
        stage: 3,
      })
    }
    if (network.shared) {
      links.push({ id: `host-net:${network.name}`, kind: 'member', from: 'host', to: switchID, d: pathOf([HOST_AT, { a: from.a, b: HOST_AT.b }, from]), stage: 2 })
    }
  }

  for (const service of allServices) {
    const id = `svc:${service.key}`
    const to = positions[id]
    if (!to) continue
    const published = service.containers.flatMap((container) => (container.ports ?? []).filter((port) => port.host_port))
    const reachable = published.filter((port) => !isLoopback(port.host_ip))
    const loopback = published.filter((port) => isLoopback(port.host_ip))
    if (reachable.length > 0) {
      links.push({ id: `publish:${service.key}`, kind: 'publish', from: 'host', to: id, d: laneRoute(HOST_AT, to, PUBLISH_LANE), stage: 3 })
    }
    if (loopback.length > 0) {
      links.push({ id: `loopback:${service.key}`, kind: 'loopback', from: 'host', to: id, d: laneRoute(HOST_AT, to, PUBLISH_LANE), stage: 3 })
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

// routeBetween은 실제로 일어난 연결을 그릴 경로를 만든다.
export function routeBetween(scene: Scene, from: string, to: string): string | undefined {
  const start = scene.positions[from]
  const end = scene.positions[to]
  if (!start || !end) return undefined
  return laneRoute(start, end, PUBLISH_LANE)
}

export type ServiceRole = 'database' | 'cache' | 'proxy' | 'storage' | 'web' | 'app'

// serviceRole은 아이콘 모양을 고르기 위한 추정이다. 이미지와 서비스 이름만 보며 다른 판단에는 쓰지 않는다.
export function serviceRole(service: Service): ServiceRole {
  const image = service.containers[0]?.image.toLowerCase() ?? ''
  if (/postgres|mysql|mariadb|mongo|cockroach|timescale|clickhouse/.test(image)) return 'database'
  if (/redis|valkey|memcached|keydb/.test(image)) return 'cache'
  if (/nginx|caddy|traefik|haproxy|envoy/.test(image)) return 'proxy'
  if (/minio|seaweed|garage|ceph/.test(image)) return 'storage'
  if (/\b(web|front|ui|site)\b/.test(`${service.name} ${image}`.replace(/[-_/:]/g, ' '))) return 'web'
  return 'app'
}
