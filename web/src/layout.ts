import type { Network, Topology } from './api'

export const CARD_W = 232
export const CARD_H = 106
const CARD_GAP = 12
const GROUP_PAD = 12
const GROUP_HEADER = 40
const COLUMN_GAP = 56
const NETWORK_BAND = 26
export const HOST_H = 84
const RAIL_H = 26
const RAIL_GAP = 8
const MIN_RAIL_ROWS = 2

export const GROUP_W = CARD_W + GROUP_PAD * 2

// SlotMemory는 프로젝트 열과 서비스 칸의 배정 기록이다.
// 한 번 배정한 자리는 재정렬 전까지 바꾸지 않아 갱신 때 노드가 움직이지 않는다.
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
      if (!slots.includes(service.key)) slots.push(service.key)
    }
  }
  return next
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export interface MapLayout {
  host: Box
  rails: Record<string, Box>
  groups: Record<string, Box>
  services: Record<string, Box>
  bands: Record<string, Box>
}

export function computeLayout(
  topology: Topology,
  slots: SlotMemory,
  collapsed: ReadonlySet<string>,
): MapLayout {
  const columns = Math.max(slots.projects.length, 1)
  const totalWidth = columns * GROUP_W + (columns - 1) * COLUMN_GAP
  const sharedNetworks = topology.networks.filter((network) => network.shared)
  const railRows = Math.max(sharedNetworks.length, MIN_RAIL_ROWS)
  const railTop = HOST_H + RAIL_GAP * 2
  const groupTop = railTop + railRows * (RAIL_H + RAIL_GAP) + RAIL_GAP

  const layout: MapLayout = {
    host: { x: 0, y: 0, width: totalWidth, height: HOST_H },
    rails: {},
    groups: {},
    services: {},
    bands: {},
  }

  sharedNetworks.forEach((network, index) => {
    layout.rails[network.name] = {
      x: 0,
      y: railTop + index * (RAIL_H + RAIL_GAP),
      width: totalWidth,
      height: RAIL_H,
    }
  })

  const present = new Set(topology.projects.map((project) => project.key))
  slots.projects.forEach((projectKey, column) => {
    if (!present.has(projectKey)) return
    const x = column * (GROUP_W + COLUMN_GAP)
    const bands = localNetworks(topology.networks, projectKey)
    const isCollapsed = collapsed.has(projectKey)
    const serviceSlots = slots.services[projectKey] ?? []
    const contentHeight = isCollapsed ? 0 : serviceSlots.length * (CARD_H + CARD_GAP) + bands.length * NETWORK_BAND
    layout.groups[projectKey] = {
      x,
      y: groupTop,
      width: GROUP_W,
      height: GROUP_HEADER + contentHeight + (isCollapsed ? 0 : GROUP_PAD),
    }
    if (isCollapsed) return

    serviceSlots.forEach((serviceKey, row) => {
      layout.services[serviceKey] = {
        x: x + GROUP_PAD,
        y: groupTop + GROUP_HEADER + row * (CARD_H + CARD_GAP),
        width: CARD_W,
        height: CARD_H,
      }
    })
    const bandTop = groupTop + GROUP_HEADER + serviceSlots.length * (CARD_H + CARD_GAP)
    bands.forEach((network, index) => {
      layout.bands[network.name] = {
        x: x + GROUP_PAD,
        y: bandTop + index * NETWORK_BAND,
        width: CARD_W,
        height: NETWORK_BAND - 4,
      }
    })
  })
  return layout
}

// localNetworks는 한 프로젝트에서만 쓰는 네트워크다. 그룹 안쪽 띠로 그린다.
export function localNetworks(networks: Network[], projectKey: string): Network[] {
  return networks.filter(
    (network) => !network.shared && network.members.some((member) => member.startsWith(`${projectKey}/`)),
  )
}
