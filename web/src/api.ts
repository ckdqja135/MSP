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

export function fetchRequests(server: string, from: string, to: string, limit: number) {
  return get<RequestRow[]>(`/servers/${encodeURIComponent(server)}/requests`, new URLSearchParams({ from, to, limit: String(limit) }))
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
