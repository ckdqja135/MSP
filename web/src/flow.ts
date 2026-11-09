import type { HostService, Probe, RequestRow, Service, Topology } from './api'
import { parseTarget } from './context'
import { isLoopbackAddress } from './format'
import { serviceRole } from './scene'
import { hostServiceKey, hostServiceName, serviceLabel } from './status'

// 통신 재생의 대본과 시간표다. 그리는 코드와 분리해 시간을 인자로 받아 시험할 수 있게 한다.

export type FlowSource = 'requests' | 'observed' | 'simulation'
export type PacketKind = 'connect' | 'request' | 'response' | 'probe' | 'fail'

export const PACKET_LABEL: Record<PacketKind, string> = {
  connect: '연결',
  request: '요청',
  response: '응답',
  probe: '응답 확인',
  fail: '실패',
}

export interface FlowPacket {
  id: string
  kind: PacketKind
  protocol: string
  title: string
  detail: string
  // 지나가는 장비를 순서대로 적는다.
  path: string[]
  from: string
  to: string
  // 측정하지 않은 값은 null이다. 화면에는 비워서 표시한다.
  at: string | null
  durationMS: number | null
  bytes: number | null
  result: string
}

export interface FlowStep {
  id: string
  title: string
  summary: string
  description: string
  packets: FlowPacket[]
}

export interface FlowScript {
  source: FlowSource
  title: string
  note: string
  steps: FlowStep[]
  facts: [string, string][]
}

const KNOWN_PORTS: Record<number, string> = {
  80: 'HTTP',
  443: 'HTTPS',
  3000: 'HTTP',
  3306: 'MySQL',
  5000: 'HTTP',
  5432: 'PostgreSQL',
  6379: 'Redis',
  8000: 'HTTP',
  8080: 'HTTP',
  8443: 'HTTPS',
  9000: 'HTTP',
  27017: 'MongoDB',
}

const isWeb = (protocol: string) => protocol === 'HTTP' || protocol === 'HTTPS'
const requestTitle = (protocol: string) => (isWeb(protocol) ? 'GET /' : '요청')
const responseTitle = (protocol: string) => (isWeb(protocol) ? '200 OK' : '응답')

function protocolOf(port: number | undefined): string {
  return (port !== undefined && KNOWN_PORTS[port]) || 'TCP'
}

// 안쪽 서비스의 프로토콜은 알려진 포트나 이미지로 짐작한다. 시뮬레이션의 표시에만 쓴다.
function internalProtocol(service: Service): string {
  const port = service.containers.flatMap((container) => container.ports ?? []).find((item) => KNOWN_PORTS[item.container_port])
  if (port) return KNOWN_PORTS[port.container_port]
  const role = serviceRole(service)
  if (role === 'database') return 'DB'
  if (role === 'cache') return 'Cache'
  return role === 'web' || role === 'proxy' ? 'HTTP' : 'TCP'
}

function preferredPort(ports: number[]): number | undefined {
  if (ports.length === 0) return undefined
  for (const port of [443, 80]) if (ports.includes(port)) return port
  return Math.min(...ports)
}

function publicPorts(service: Service): number[] {
  return service.containers.flatMap((container) =>
    (container.ports ?? []).filter((port) => port.host_port && !isLoopbackAddress(port.host_ip)).map((port) => port.host_port!),
  )
}

function hostPublicPorts(service: HostService): number[] {
  return service.listening ? service.ports.filter((port) => !isLoopbackAddress(port.ip)).map((port) => port.port) : []
}

// 선언된 의존을 따라 가장 긴 경로를 찾는다. 선언은 실제 호출을 보장하지 않으므로 시뮬레이션에만 쓴다.
function longestChain(start: Service, topology: Topology, services: Map<string, Service>, must?: string): Service[] {
  const edges = topology.relations.filter((relation) => relation.type === 'declared_depends_on')
  let best: Service[] = [start]
  const better = (path: Service[]) => {
    const hasMust = (list: Service[]) => (must ? list.some((item) => item.key === must) : false)
    if (hasMust(path) !== hasMust(best)) return hasMust(path)
    return path.length > best.length
  }
  const walk = (path: Service[]) => {
    if (better(path)) best = path
    const last = path[path.length - 1]
    const next = edges
      .filter((relation) => relation.from === last.key)
      .map((relation) => services.get(relation.to))
      .filter((item): item is Service => Boolean(item) && item!.runtime !== 'stopped' && !path.includes(item!))
      .sort((a, b) => a.key.localeCompare(b.key))
    for (const item of next) walk([...path, item])
  }
  walk([start])
  return best
}

interface Entry {
  id: string
  name: string
  port: number
  chain: Service[]
  // 호스트 서비스는 어디로 전달하는지 설정을 읽지 않아 알 수 없다.
  host: boolean
  // 고른 서비스가 입구의 선언 경로에 없으면 그 서비스로 전달된다고 가정해 잇는다.
  assumed?: Service
}

function findEntry(topology: Topology, target?: string): Entry | undefined {
  const services = new Map(topology.projects.flatMap((project) => project.services).map((service) => [service.key, service]))
  const parsed = parseTarget(target)
  const must = parsed?.kind === 'service' ? parsed.key : undefined
  const mustHost = parsed?.kind === 'hostService' ? parsed.name : undefined
  const entries: Entry[] = []
  for (const service of services.values()) {
    const port = preferredPort(publicPorts(service))
    if (port === undefined || service.runtime === 'stopped') continue
    entries.push({ id: `svc:${service.key}`, name: serviceLabel(service.key), port, chain: longestChain(service, topology, services, must), host: false })
  }
  for (const service of topology.host_services ?? []) {
    const port = preferredPort(hostPublicPorts(service))
    if (port === undefined || !(port === 80 || port === 443 || mustHost === hostServiceKey(service))) continue
    entries.push({ id: `hostsvc:${hostServiceKey(service)}`, name: hostServiceName(service), port, chain: [], host: true })
  }
  const score = (entry: Entry) => [
    (must && entry.chain.some((item) => item.key === must)) || (mustHost && entry.id === `hostsvc:${mustHost}`) ? 0 : 1,
    -entry.chain.length,
    entry.port === 443 || entry.port === 80 ? 0 : 1,
  ]
  const best = entries.sort((a, b) => {
    const [x, y] = [score(a), score(b)]
    for (let i = 0; i < x.length; i++) if (x[i] !== y[i]) return x[i] - y[i]
    return a.id.localeCompare(b.id)
  })[0]
  const chosen = must ? services.get(must) : undefined
  if (best && chosen && chosen.runtime !== 'stopped' && !best.chain.includes(chosen)) {
    return { ...best, chain: longestChain(chosen, topology, services), assumed: chosen }
  }
  return best
}

const SIMULATION_RESULT = '예시'

// simulationScript는 구성을 따라 예시 통신을 만든다. 측정값이 없으므로 시간과 소요는 비워 둔다.
export function simulationScript(topology: Topology, target?: string): FlowScript | undefined {
  const entry = findEntry(topology, target)
  if (!entry) return undefined
  const protocol = protocolOf(entry.port)
  const inward = ['internet', 'host', entry.id]
  const outward = [...inward].reverse()
  const packet = (id: string, kind: PacketKind, protocolName: string, title: string, detail: string, path: string[], from: string, to: string): FlowPacket => ({
    id, kind, protocol: protocolName, title, detail, path, from, to, at: null, durationMS: null, bytes: null, result: SIMULATION_RESULT,
  })

  const steps: FlowStep[] = [{
    id: 'connect',
    title: '연결',
    summary: `포트 ${entry.port}로 TCP 연결`,
    description: `외부 클라이언트가 공개 포트 ${entry.port}로 들어와 ${entry.name}와 TCP 연결을 맺습니다.`,
    packets: [
      packet('syn', 'connect', 'TCP', 'SYN', `외부 → ${entry.name}:${entry.port}`, inward, '외부', entry.name),
      packet('synack', 'connect', 'TCP', 'SYN, ACK', `${entry.name} → 외부`, outward, entry.name, '외부'),
      packet('ack', 'connect', 'TCP', 'ACK', '연결 완료', inward, '외부', entry.name),
    ],
  }]

  // 입구 뒤로 이어지는 장비다. 입구가 서비스면 chain의 첫 항목이 입구 자신이다.
  const tail = entry.host || entry.assumed ? entry.chain : entry.chain.slice(1)
  const inner = [
    { id: entry.id, name: entry.name, short: entry.name },
    ...tail.map((service) => ({ id: `svc:${service.key}`, name: serviceLabel(service.key), short: service.name })),
  ]
  const requests = [packet('req', 'request', protocol, requestTitle(protocol), `Host 포트 ${entry.port}`, inward, '외부', entry.name)]
  const responses: FlowPacket[] = []
  tail.forEach((service, index) => {
    const [last, next] = [inner[index], inner[index + 1]]
    const name = internalProtocol(service)
    requests.push(packet(`req-${index}`, 'request', name, requestTitle(name), `${last.short} → ${next.short}`, [last.id, next.id], last.name, next.name))
    responses.unshift(packet(`res-${index}`, 'response', name, responseTitle(name), `${next.short} → ${last.short}`, [next.id, last.id], next.name, last.name))
  })
  responses.push(packet('res', 'response', protocol, responseTitle(protocol), `${entry.name} → 외부`, outward, entry.name, '외부'))

  let description = `요청이 ${entry.name}에 도착합니다. 선언된 의존이 없어 안쪽으로 이어 그리지 않습니다.`
  if (entry.assumed) {
    description = `요청이 ${entry.name}에 도착한 뒤 고른 서비스 ${serviceLabel(entry.assumed.key)}로 전달된다고 가정합니다. ${entry.name}의 실제 전달 설정은 읽지 않습니다.`
  } else if (entry.host) {
    description = `요청이 ${entry.name}에 도착합니다. ${entry.name}가 어디로 전달하는지는 설정을 읽지 않아 그리지 않습니다. 서비스를 고르면 그 서비스까지 이어 그립니다.`
  } else if (tail.length > 0) {
    description = `요청이 ${entry.name}에 도착한 뒤 Compose에 선언된 의존 순서대로 전달된다고 가정합니다. 선언은 실제 호출을 보장하지 않습니다.`
  }

  steps.push({
    id: 'request',
    title: '요청',
    summary: tail.length > 0 ? '안쪽 서비스까지 전달' : `${entry.name}에 도착`,
    description,
    packets: requests,
  }, {
    id: 'response',
    title: '응답',
    summary: '들어온 길을 되돌아 나감',
    description: '응답이 들어온 길을 되돌아 외부로 나갑니다.',
    packets: responses,
  })

  const count = steps.reduce((total, step) => total + step.packets.length, 0)
  // 시뮬레이션은 정상 흐름만 그리므로 지금 문제가 있는 서비스를 함께 알린다.
  const troubled = tail.filter((service) => service.status === 'critical').map((service) => service.name)
  return {
    source: 'simulation',
    title: `${entry.name}로 들어오는 요청`,
    note: '실제 통신이 아닙니다. 공개 포트와 Compose 선언을 따라 그린 예시이며, 시간과 소요는 측정하지 않아 비워 둡니다.',
    steps,
    facts: [
      ['입구', `${entry.name}:${entry.port}`],
      ['경로', inner.map((item) => item.short).join(' → ')],
      ['패킷', `${count}개 (예시)`],
      ...(troubled.length > 0 ? ([['현재 장애', `${troubled.join(', ')}. 예시는 정상 흐름만 그립니다`]] as [string, string][]) : []),
    ],
  }
}

function probePath(probe: Probe, service: Service): string[] {
  return [probe.vantage === 'external' ? 'internet' : 'host', `svc:${service.key}`]
}

const probeTitle = (probe: Probe) => probe.target.replace(/^GET https?:\/\/[^/]+/, 'GET ')
const probeOrigin = (probe: Probe) => (probe.vantage === 'external' ? '외부 프로브' : '에이전트')

function probeStep(service: Service, probes: Probe[]): FlowStep {
  const name = serviceLabel(service.key)
  const packets: FlowPacket[] = []
  for (const probe of probes) {
    const path = probePath(probe, service)
    const base = { protocol: 'HTTP', at: probe.last_checked, bytes: null }
    const key = `${service.key}:${probe.vantage}`
    const origin = probeOrigin(probe)
    if (probe.state === 'ok') {
      packets.push(
        { ...base, id: `${key}:req`, kind: 'probe', title: probeTitle(probe), detail: `${origin} → ${name}`, path, from: origin, to: name, durationMS: null, result: '전송' },
        { ...base, id: `${key}:res`, kind: 'response', title: '응답', detail: `${name} → ${origin}`, path: [...path].reverse(), from: name, to: origin, durationMS: probe.latency_ms, result: '정상' },
      )
    } else {
      packets.push({
        ...base, id: `${key}:fail`, kind: 'fail', title: probeTitle(probe), detail: probe.last_failure || '응답 없음', path, from: origin, to: name, durationMS: probe.latency_ms, result: `실패 ${probe.failures}회`,
      })
    }
  }
  const failed = probes.filter((probe) => probe.state !== 'ok').length
  return {
    id: service.key,
    title: name,
    summary: failed > 0 ? `응답 실패 ${failed}건` : '응답 정상',
    description: failed > 0 ? `${name}의 응답 확인 요청이 실패했습니다. 응답이 돌아오지 않아 돌아오는 패킷을 그리지 않습니다.` : `${name}의 응답 확인 요청과 응답입니다. 소요 시간은 왕복 기준입니다.`,
    packets,
  }
}

// observedScript는 실제로 보낸 응답 확인 요청의 마지막 결과를 재생한다. 사용자 요청은 수집하지 않는다.
export function observedScript(topology: Topology, target?: string, demo = false): FlowScript | undefined {
  const parsed = parseTarget(target)
  const services = topology.projects
    .flatMap((project) => project.services)
    .filter((service) => service.probes.length > 0 && (parsed?.kind !== 'service' || service.key === parsed.key))
    .sort((a, b) => Math.min(...a.probes.map((probe) => Date.parse(probe.last_checked))) - Math.min(...b.probes.map((probe) => Date.parse(probe.last_checked))))
  if (services.length === 0) return undefined
  const steps = services.map((service) => probeStep(service, service.probes))
  const probes = services.flatMap((service) => service.probes)
  const failed = probes.filter((probe) => probe.state !== 'ok').length
  return {
    source: 'observed',
    title: '응답 확인 기록',
    note: demo
      ? '데모용 가짜 기록입니다. 실제 서버에 연결하면 실제로 보낸 응답 확인 요청의 결과가 나옵니다.'
      : '실제로 보낸 응답 확인 요청의 마지막 결과입니다. 사용자의 요청은 수집하지 않아 여기에 나오지 않습니다.',
    steps,
    facts: [
      ['대상', `서비스 ${services.length}개`],
      ['요청', `${probes.length}건 (실패 ${failed}건)`],
      ['크기', '측정하지 않음'],
    ],
  }
}

// entryDevice는 요청을 받은 프록시의 장비를 찾는다. 구성에 없으면 서버까지만 그린다.
export function entryDevice(service: string, topology: Topology): string {
  if ((topology.host_services ?? []).some((item) => hostServiceKey(item) === service)) return `hostsvc:${service}`
  if (topology.projects.some((project) => project.services.some((item) => item.key === service))) return `svc:${service}`
  return 'host'
}

// acceptsLocal은 서버 자신에게 보낸 연결을 그 바인딩이 받는지 본다. 다른 주소에만 묶인 포트는 받지 않는다.
function acceptsLocal(bind: string | undefined, host: string): boolean {
  return !bind || bind === host || bind === '::' || (bind === '0.0.0.0' && !host.includes(':'))
}

// upstreamDevice는 로그에 적힌 전달 주소를 구성에서 찾는다. 찾지 못하면 뒤쪽을 그리지 않는다.
export function upstreamDevice(upstream: string, topology: Topology): { id: string; name: string } | undefined {
  const index = upstream.lastIndexOf(':')
  if (index < 0) return undefined
  const host = upstream.slice(0, index).replace(/^\[(.*)\]$/, '$1')
  const port = Number(upstream.slice(index + 1))
  const loopback = isLoopbackAddress(host)
  for (const service of topology.projects.flatMap((project) => project.services)) {
    for (const container of service.containers) {
      const byAddress = container.networks?.some((network) => network.ip === host)
      const byPublished = loopback && container.ports?.some((item) => item.host_port === port && acceptsLocal(item.host_ip, host))
      if (byAddress || byPublished) return { id: `svc:${service.key}`, name: serviceLabel(service.key) }
    }
  }
  if (!loopback) return undefined
  const owner = (topology.host_services ?? []).find((item) => item.listening && item.ports.some((bind) => bind.port === port && acceptsLocal(bind.ip, host)))
  return owner ? { id: `hostsvc:${hostServiceKey(owner)}`, name: hostServiceName(owner) } : undefined
}

const MAX_REQUEST_STEPS = 8

export function requestStep(request: RequestRow, topology: Topology): FlowStep {
  const label = `${request.method} ${request.path}`
  const entry = entryDevice(request.service, topology)
  const inward = entry === 'host' ? ['internet', 'host'] : ['internet', 'host', entry]
  const protocol = request.proto || 'HTTP'
  const failed = request.status >= 500
  const found = upstreamDevice(request.upstream, topology)
  const target = found?.id === entry ? undefined : found
  const key = String(request.id)
  const base = { protocol, at: request.at }
  const packets: FlowPacket[] = [
    { ...base, id: `${key}:in`, kind: 'request', title: label, detail: `외부 → ${request.service}`, path: inward, from: '외부', to: request.service, durationMS: null, bytes: null, result: '수신' },
  ]
  if (target) {
    packets.push({ ...base, id: `${key}:forward`, kind: 'request', title: label, detail: `${request.service} → ${target.name}`, path: [entry, target.id], from: request.service, to: target.name, durationMS: null, bytes: null, result: '전달' })
    if (request.upstream_ms !== null) {
      packets.push({ ...base, id: `${key}:back`, kind: 'response', title: '응답', detail: `${target.name} → ${request.service}`, path: [target.id, entry], from: target.name, to: request.service, durationMS: request.upstream_ms, bytes: null, result: '응답' })
    }
  }
  packets.push({
    ...base, id: `${key}:out`, kind: failed ? 'fail' : 'response', title: String(request.status), detail: `${request.service} → 외부`, path: [...inward].reverse(), from: request.service, to: '외부', durationMS: request.duration_ms, bytes: request.bytes, result: String(request.status),
  })

  let description = `접근 로그에 전달 대상이 적혀 있지 않아 ${request.service} 뒤쪽은 그리지 않습니다.`
  if (target) {
    description = `${request.service}가 ${target.name}로 전달한 요청입니다.`
  } else if (found) {
    description = `${request.service}가 자기 자신(${request.upstream})으로 다시 보낸 요청입니다.`
  } else if (request.upstream) {
    description = `전달 주소 ${request.upstream}를 지금 구성에서 찾지 못해 ${request.service} 뒤쪽은 그리지 않습니다.`
  }
  if (failed) description += ` 응답 코드 ${request.status}입니다.`
  return {
    id: key,
    title: label,
    summary: [String(request.status), request.duration_ms !== null ? `${request.duration_ms}ms` : ''].filter(Boolean).join(' · '),
    description,
    packets,
  }
}

// requestScript는 접근 로그에서 읽은 최근 요청을 재생한다. 요청 사이의 간격은 실제 시간이 아니다.
export function requestScript(requests: RequestRow[], topology: Topology, demo = false): FlowScript | undefined {
  if (requests.length === 0) return undefined
  const recent = [...requests].sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.id - b.id).slice(-MAX_REQUEST_STEPS)
  const failed = requests.filter((request) => request.status >= 500).length
  const forwarded = requests.filter((request) => request.upstream)
  let note = demo
    ? '데모용 가짜 요청입니다. 실제 서버에 연결하면 접근 로그에서 읽은 실제 요청이 나옵니다.'
    : '접근 로그에서 읽은 실제 요청입니다. 쿼리 문자열과 클라이언트 주소는 저장하지 않습니다. 서비스끼리 주고받는 호출은 이 로그에 없어 보이지 않습니다.'
  if (!demo && forwarded.length === 0) {
    note += ' 지금 요청에는 전달 주소가 없어 프록시 뒤 컨테이너는 그리지 않습니다. nginx 로그 형식에 ua="$upstream_addr"를 덧붙이면 이어집니다.'
  }
  return {
    source: 'requests',
    title: `최근 API 요청 ${recent.length}건`,
    note,
    steps: recent.map((request) => requestStep(request, topology)),
    facts: [
      ['조회한 요청', `${requests.length}건 (5xx ${failed}건)`],
      ['전달 대상', forwardSummary(requests.length, forwarded, topology)],
      ['재생', `최근 ${recent.length}건`],
      ['간격', '실제 시간과 다릅니다'],
    ],
  }
}

// forwardSummary는 전달 주소를 구성에서 몇 건 찾았는지 센다. 전달 주소가 없으면 직접 응답했거나 로그 형식에 값이 없는 것이다.
function forwardSummary(total: number, forwarded: RequestRow[], topology: Topology): string {
  if (forwarded.length === 0) return '로그에 기록 없음'
  const found = forwarded.filter((request) => upstreamDevice(request.upstream, topology)).length
  return [
    `${found}건 확인`,
    forwarded.length > found ? `${forwarded.length - found}건 구성에서 못 찾음` : '',
    total > forwarded.length ? `${total - forwarded.length}건 기록 없음` : '',
  ].filter(Boolean).join(' · ')
}

// 실시간으로 보여주는 요청의 수다. 넘으면 오래된 것부터 뺀다.
export const LIVE_MAX_STEPS = 30

export function liveRequestScript(steps: FlowStep[], demo = false): FlowScript {
  return {
    source: 'requests',
    title: '실시간 API 요청',
    note: demo
      ? '데모용 가짜 요청을 들어오는 대로 보여줍니다.'
      : '접근 로그에 새로 적힌 요청을 들어오는 대로 보여줍니다. 수집과 전송 때문에 몇 초 늦게 나타납니다.',
    steps,
    facts: [
      ['보여준 요청', `${steps.length}건`],
      ['지연', '실제보다 몇 초 늦음'],
    ],
  }
}

// appendLiveSteps는 마지막으로 본 요청 뒤에 들어온 것만 덧붙인다. 넘치면 앞에서 뺀 수를 함께 돌려준다.
export function appendLiveSteps(steps: FlowStep[], requests: RequestRow[], topology: Topology, lastID: number): { steps: FlowStep[]; dropped: number; lastID: number } {
  const fresh = requests.filter((request) => request.id > lastID).sort((a, b) => a.id - b.id)
  if (fresh.length === 0) return { steps, dropped: 0, lastID }
  const all = [...steps, ...fresh.map((request) => requestStep(request, topology))]
  const dropped = Math.max(0, all.length - LIVE_MAX_STEPS)
  return { steps: all.slice(dropped), dropped, lastID: fresh[fresh.length - 1].id }
}

const STEP_INTRO_MS = 1100
const HOP_MS = 620
const ARRIVE_MS = 420

export type SegmentKind = 'intro' | 'hop' | 'arrive'

export interface Segment {
  kind: SegmentKind
  start: number
  duration: number
  step: number
  // 연번은 표의 행 번호와 같다.
  packet: number
  hop: number
}

export interface Timeline {
  script: FlowScript
  packets: FlowPacket[]
  // 패킷 연번별 소속 단계다.
  stepOf: number[]
  segments: Segment[]
  // 패킷 연번별 도착 시각이다.
  arrivals: number[]
  // 단계별 시작 시각이다.
  stepStarts: number[]
  total: number
}

export function buildTimeline(script: FlowScript): Timeline {
  const segments: Segment[] = []
  const packets: FlowPacket[] = []
  const stepOf: number[] = []
  const arrivals: number[] = []
  const stepStarts: number[] = []
  let clock = 0
  const push = (kind: SegmentKind, duration: number, step: number, packet: number, hop: number) => {
    segments.push({ kind, start: clock, duration, step, packet, hop })
    clock += duration
  }
  script.steps.forEach((step, stepIndex) => {
    stepStarts.push(clock)
    push('intro', STEP_INTRO_MS, stepIndex, packets.length, -1)
    for (const packet of step.packets) {
      const number = packets.length
      packets.push(packet)
      stepOf.push(stepIndex)
      for (let hop = 0; hop < packet.path.length - 1; hop++) push('hop', HOP_MS, stepIndex, number, hop)
      arrivals.push(clock)
      push('arrive', ARRIVE_MS, stepIndex, number, packet.path.length - 2)
    }
  })
  return { script, packets, stepOf, segments, arrivals, stepStarts, total: clock }
}

export interface FlowFrame {
  phase: SegmentKind | 'overview' | 'done'
  step: number
  packet: number
  hop: number
  // 도착한 패킷 수다. 표에는 이만큼만 보인다.
  delivered: number
  revealed: string[]
  camera: string[]
}

function allDevices(timeline: Timeline): string[] {
  return [...new Set(timeline.packets.flatMap((packet) => packet.path))]
}

export const overviewFrame = (timeline: Timeline): FlowFrame => ({
  phase: 'overview', step: -1, packet: -1, hop: -1, delivered: timeline.packets.length, revealed: allDevices(timeline), camera: [],
})

// frameAt은 재생 시각의 화면 상태를 계산한다. 같은 시각이면 언제나 같은 결과를 돌려준다.
export function frameAt(timeline: Timeline, t: number): FlowFrame {
  if (t >= timeline.total) {
    const all = allDevices(timeline)
    return { phase: 'done', step: timeline.script.steps.length - 1, packet: timeline.packets.length - 1, hop: -1, delivered: timeline.packets.length, revealed: all, camera: all }
  }
  const segment = timeline.segments.find((item) => t < item.start + item.duration) ?? timeline.segments[0]
  const delivered = timeline.arrivals.filter((arrival) => arrival <= t).length
  const revealed = new Set<string>()
  timeline.packets.slice(0, delivered).forEach((packet) => packet.path.forEach((id) => revealed.add(id)))
  const current = timeline.packets[segment.packet]
  let camera: string[] = []
  if (current) {
    // 처음에는 출발 장비만 가까이 보여주고, 패킷이 떠나면 경로 전체로 물러난다.
    camera = segment.kind === 'intro' && delivered === 0 ? [current.path[0]] : current.path
    if (segment.kind === 'intro') {
      revealed.add(current.path[0])
    } else {
      current.path.slice(0, segment.hop + 2).forEach((id) => revealed.add(id))
    }
  }
  return { phase: segment.kind, step: segment.step, packet: segment.packet, hop: segment.hop, delivered, revealed: [...revealed], camera }
}

// hopProgress는 지금 구간 안에서 패킷이 얼마나 갔는지 0에서 1 사이로 돌려준다.
export function hopProgress(timeline: Timeline, t: number): number {
  const segment = timeline.segments.find((item) => t < item.start + item.duration)
  if (!segment || segment.kind === 'arrive') return 1
  if (segment.kind === 'intro') return 0
  return Math.min(1, (t - segment.start) / segment.duration)
}

// nextArrival은 지금 시각 뒤에 처음 도착하는 패킷의 시각을 돌려준다. 한 패킷씩 볼 때 쓴다.
export function nextArrival(timeline: Timeline, t: number): number {
  return timeline.arrivals.find((arrival) => arrival > t) ?? timeline.total
}

// previousStepStart는 지금 단계의 처음으로, 이미 처음이면 앞 단계로 돌아간다.
export function previousStepStart(timeline: Timeline, t: number): number {
  const GRACE_MS = 800
  const earlier = timeline.stepStarts.filter((start) => start + GRACE_MS < t)
  return earlier.length > 0 ? earlier[earlier.length - 1] : 0
}
