export type Status = 'ok' | 'warning' | 'critical' | 'stopped' | 'disconnected'
export type Response = 'ok' | 'failing' | 'down' | 'unconfirmed' | 'historical' | 'external_path'

export interface LatestValue {
  value: number
  at: string
}

export interface PortBinding {
  host_ip?: string
  host_port?: number
  container_port: number
  proto: string
}

export interface Container {
  id: string
  name: string
  project: string
  service: string
  replica: number
  one_off?: boolean
  image: string
  state: string
  exit_code: number
  oom_killed?: boolean
  health: string
  restart_count: number
  restart_policy?: string
  created_at: string
  started_at: string
  networks?: { network: string; ip?: string }[]
  ports?: PortBinding[]
  declared_depends_on?: string[]
  metrics: Record<string, LatestValue>
}

export interface Probe {
  vantage: 'internal' | 'external'
  target: string
  state: 'ok' | 'failing' | 'down'
  failures: number
  last_checked: string
  latency_ms: number | null
  last_failure?: string
}

export interface Service {
  key: string
  name: string
  project: string
  status: Status
  runtime: string
  response: Response
  reasons: string[]
  containers: Container[]
  probes: Probe[]
}

export interface Project {
  key: string
  status: Status
  services: Service[]
}

export interface Network {
  id: string
  name: string
  driver: string
  subnet?: string
  internal?: boolean
  members: string[]
  shared: boolean
}

export interface Relation {
  type: 'member_of' | 'publishes' | 'declared_depends_on'
  from: string
  to: string
  evidence: string
  detail?: string
}

export interface HostServicePort {
  ip: string
  port: number
}

export interface HostService {
  name: string
  ports: HostServicePort[]
  evidence: 'socket-owner' | 'config' | 'name-match' | 'unknown'
  listening: boolean
}

export interface Topology {
  server_id: string
  version_id: number
  valid_from: string
  valid_to: string | null
  observed_at: string
  historical: boolean
  projects: Project[]
  networks: Network[]
  relations: Relation[]
  host_services?: HostService[]
}

export interface Collector {
  name: string
  state: 'ok' | 'unsupported' | 'error'
  last_success?: string
  error?: string
}

export interface Server {
  id: string
  mode: 'live' | 'demo'
  last_seen: string | null
  clock_skew_ms: number
  agent_version: string
  boot_time: string | null
  uptime_seconds: number
  docker_ok: boolean
  // Docker가 없는 컴퓨터다. 응답하지 않는 것과 달리 문제가 아니다.
  docker_absent?: boolean
  collectors: Collector[]
  reachability: { state: string; label: string; detail: string }
  metrics: Record<string, LatestValue>
}

export interface Meta {
  mode: 'live' | 'demo'
  read_only: boolean
  integrations: Record<string, string>
}

async function get<T>(path: string, params?: URLSearchParams): Promise<T> {
  const query = params && params.size > 0 ? `?${params}` : ''
  const response = await fetch(`/api/v1${path}${query}`)
  if (!response.ok) {
    throw new Error(`${response.status} ${await response.text()}`)
  }
  return response.json() as Promise<T>
}

export type EventCategory = 'ops' | 'probe' | 'resource' | 'alert' | 'gap'

export interface EventRow {
  id: number
  occurred_at: string
  received_at: string
  category: EventCategory
  type: string
  severity: 'info' | 'warning' | 'critical'
  source: string
  target: string
  service_key: string
  container_id: string
  summary: string
  duration_ms: number | null
  duration_limit_ms: number | null
  bytes: number | null
  result: string
  evidence: string
  aggregated: number
}

export interface GapRow {
  source: string
  from: string
  to: string
  reason: string
  dropped: number
}

export interface EventQuery {
  from: string
  to: string
  services?: string[]
  categories?: string[]
  errorsOnly?: boolean
  before?: number
  limit?: number
}

export const fetchMeta = () => get<Meta>('/meta')

export const fetchServers = () => get<Server[]>('/servers')

export function fetchTopology(server: string, at?: string) {
  const params = new URLSearchParams()
  if (at) params.set('at', at)
  return get<Topology>(`/servers/${encodeURIComponent(server)}/topology`, params)
}

export function fetchEvents(server: string, query: EventQuery) {
  const params = new URLSearchParams({ from: query.from, to: query.to })
  query.services?.forEach((service) => params.append('service', service))
  query.categories?.forEach((category) => params.append('category', category))
  if (query.errorsOnly) params.set('errors', 'true')
  if (query.before) params.set('before', String(query.before))
  if (query.limit) params.set('limit', String(query.limit))
  return get<EventRow[]>(`/servers/${encodeURIComponent(server)}/events`, params)
}

export function fetchGaps(server: string, from: string, to: string) {
  return get<GapRow[]>(`/servers/${encodeURIComponent(server)}/gaps`, new URLSearchParams({ from, to }))
}

// RequestRow는 프록시 접근 로그에서 읽은 요청이다. 로그에 없는 값은 null이다.
export interface RequestRow {
  id: number
  at: string
  service: string
  method: string
  path: string
  proto: string
  status: number
  bytes: number | null
  duration_ms: number | null
  upstream: string
  upstream_ms: number | null
}

// RequestFilter는 고른 대상에 닿은 요청만 중앙 서버에서 골라 받는 조건이다.
// 요청을 받은 프록시 이름이나 전달 주소 중 하나가 맞으면 받는다.
export interface RequestFilter {
  services: string[]
  // 전달 주소의 호스트다. 포트는 가리지 않는다.
  hosts: string[]
  // 서버 자신(루프백 주소)으로 전달한 포트다.
  localPorts: number[]
}

export function fetchRequests(server: string, from: string, to: string, limit: number, filter?: RequestFilter) {
  const params = new URLSearchParams({ from, to, limit: String(limit) })
  for (const service of filter?.services ?? []) params.append('service', service)
  for (const host of filter?.hosts ?? []) params.append('upstream_host', host)
  for (const port of filter?.localPorts ?? []) params.append('local_port', String(port))
  return get<RequestRow[]>(`/servers/${encodeURIComponent(server)}/requests`, params)
}

export interface SampleSeries {
  name: string
  points: { t: number; v: number }[]
}

export interface SamplesResponse {
  step_ms: number
  series: SampleSeries[]
}

export function fetchSamples(server: string, scope: string, names: string[], from: string, to: string, points: number) {
  const params = new URLSearchParams({ scope, from, to, points: String(points) })
  names.forEach((name) => params.append('name', name))
  return get<SamplesResponse>(`/servers/${encodeURIComponent(server)}/samples`, params)
}

export interface Alert {
  id: number
  server_id: string
  rule: string
  target: string
  severity: 'info' | 'warning' | 'critical'
  state: 'firing' | 'flapping' | 'resolved'
  title: string
  detail: string
  fired_at: string | null
  resolved_at: string | null
  last_notified_at: string | null
  notify_count: number
}

export const fetchAlerts = () => get<Alert[]>('/alerts')

// ScreenView는 서버 화면 도우미의 상태다. 화면 그림은 screenFrameURL로 따로 받는다.
// 이 조회가 이어지는 동안만 도우미가 화면을 읽는다.
export interface ScreenView {
  helper: 'online' | 'offline'
  session: string
  state: '' | 'ready' | 'unsupported' | 'error'
  detail: string
  seq: number
  captured_at: string | null
  width: number
  height: number
  // control은 이 서버가 원격 조작을 허용하는지, control_live는 지금 입력이 서버로 전달되는지다.
  control: boolean
  control_live: boolean
}

// control이 '1'이면 조작을 켜 둔 것이고 '0'이면 방금 끈 것이다. 없으면 보기만 한다.
export function fetchScreen(server: string, control?: '0' | '1') {
  const params = new URLSearchParams()
  if (control) params.set('control', control)
  return get<ScreenView>(`/servers/${encodeURIComponent(server)}/screen`, params)
}

// ScreenInput은 서버로 보내는 마우스나 키보드 입력 하나다. 위치는 화면 전체에 대한 비율(0~1)이다.
// code는 글자가 아니라 키 자리 이름이라 서버의 키보드 배열과 입력기가 그대로 적용된다.
export interface ScreenInput {
  type: 'move' | 'down' | 'up' | 'wheel' | 'keydown' | 'keyup'
  x?: number
  y?: number
  button?: number
  dx?: number
  dy?: number
  code?: string
}

// sendScreenInput은 조작을 허용한 서버에, 조작을 켠 동안에만 받아들여진다.
export async function sendScreenInput(server: string, inputs: ScreenInput[]): Promise<void> {
  const response = await fetch(`/api/v1/servers/${encodeURIComponent(server)}/screen/input`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ inputs }),
  })
  if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
}

// seq는 새 화면이 올 때마다 주소를 바꿔 브라우저가 다시 받게 한다.
export const screenFrameURL = (server: string, seq: number) => `/api/v1/servers/${encodeURIComponent(server)}/screen/frame?seq=${seq}`

// ConsoleScreen은 가상 터미널의 글자 화면이다. 읽지 못했으면 error에 이유가 온다.
export interface ConsoleScreen {
  tty: number
  active: number
  rows: number
  cols: number
  cursor_row: number
  cursor_col: number
  lines: string[] | null
  error?: string
}

export function fetchConsole(server: string, tty: number) {
  return get<ConsoleScreen>(`/servers/${encodeURIComponent(server)}/console`, new URLSearchParams({ tty: String(tty) }))
}
