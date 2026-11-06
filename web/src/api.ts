export type Status = 'ok' | 'warning' | 'critical' | 'stopped' | 'disconnected'
export type Response = 'ok' | 'failing' | 'down' | 'unconfirmed' | 'historical'

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

export const fetchMeta = () => get<Meta>('/meta')

export const fetchServers = () => get<Server[]>('/servers')

export function fetchTopology(server: string, at?: string) {
  const params = new URLSearchParams()
  if (at) params.set('at', at)
  return get<Topology>(`/servers/${encodeURIComponent(server)}/topology`, params)
}
