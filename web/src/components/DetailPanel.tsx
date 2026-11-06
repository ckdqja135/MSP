import type { ReactNode } from 'react'
import type { Container, EventRow, Meta, Network, Project, Server, Service, Topology } from '../api'
import { parseTarget, updateContext } from '../context'
import { formatAge, formatBytes, formatClock, formatDateTime, formatDuration, formatPercent, formatRate, formatSize, formatUptime, isLateArrival, readMetric, shortID } from '../format'
import { STATUS_LABEL, evidenceLabel, projectLabel, serviceHeadline, sourceLabel, typeLabel } from '../status'
import { StatusIcon, UnsupportedIcon } from './Icons'

interface Props {
  meta?: Meta
  server?: Server
  topology?: Topology
  target?: string
  now: number
  selectedEvent?: EventRow
  events: EventRow[]
}

const NEARBY_MS = 60_000

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="detail-row">
      <span className="detail-label">{label}</span>
      <span className="detail-value">{children}</span>
    </div>
  )
}

function ReadOnlyNotice() {
  return (
    <section className="detail-section">
      <h3>관리</h3>
      <p className="muted">이 버전은 읽기 전용입니다. 시작, 중지, 재시작 기능이 없습니다.</p>
    </section>
  )
}

// EventDetail은 선택한 기록과 같은 시간대의 기록을 보여준다. 시간이 가까운 것을 원인으로 표현하지 않는다.
function EventDetail({ event, events }: { event: EventRow; events: EventRow[] }) {
  const at = Date.parse(event.occurred_at)
  const nearby = events.filter((other) => other.id !== event.id && Math.abs(Date.parse(other.occurred_at) - at) <= NEARBY_MS)
  const moment = () => {
    const from = new Date(at - 10 * 60_000).toISOString()
    const to = new Date(Math.min(at + 10 * 60_000, Date.now())).toISOString()
    updateContext({ from, to, at: event.occurred_at, frozen: undefined })
  }
  return (
    <section className="detail-section event-detail">
      <h3>선택한 기록</h3>
      <div className="headline">{event.summary}</div>
      <Row label="발생">{formatDateTime(event.occurred_at)}</Row>
      <Row label="수신">
        {formatDateTime(event.received_at)}
        {isLateArrival(event) && <span className="late-tag">지연 도착</span>}
      </Row>
      <Row label="출발">{sourceLabel(event.source)}</Row>
      <Row label="도착"><span className="mono">{event.target || '—'}</span></Row>
      <Row label="유형">{typeLabel(event.type)}</Row>
      <Row label="소요">{formatDuration(event)}</Row>
      <Row label="크기">{formatSize(event)}</Row>
      <Row label="결과">{event.result || '—'}</Row>
      <Row label="근거">{evidenceLabel(event.evidence)}</Row>
      {event.container_id && <Row label="컨테이너"><span className="mono">{shortID(event.container_id)}</span></Row>}
      <div className="detail-links">
        {event.service_key && (
          <button type="button" className="tool-button" onClick={() => updateContext({ target: `svc:${event.service_key}` })}>이 대상으로 좁히기</button>
        )}
        <button type="button" className="tool-button" onClick={moment}>이 시점 구조 보기</button>
        <button type="button" className="tool-button" onClick={() => updateContext({ event: undefined })}>선택 해제</button>
      </div>
      <h3 className="nearby-title">같은 시간대 기록 (앞뒤 1분)</h3>
      <p className="muted small">시간이 가까운 기록입니다. 원인 관계를 뜻하지 않습니다.</p>
      {nearby.length === 0 && <p className="muted small">이 조회 범위에는 없습니다.</p>}
      {nearby.slice(0, 8).map((other) => (
        <button key={other.id} type="button" className="nearby-row" onClick={() => updateContext({ event: String(other.id) })}>
          <span className="mono">{formatClock(other.occurred_at)}</span>
          <span className="mono">{other.target || '—'}</span>
          <span className="ellipsis">{typeLabel(other.type)}</span>
        </button>
      ))}
    </section>
  )
}

function collectorText(state: string): string {
  if (state === 'ok') return '정상'
  if (state === 'unsupported') return '미지원'
  return '수집 실패'
}

// HostDetail은 값이 없는 이유를 수집기 상태로 구분해 표시한다. 미지원과 수집 실패를 0으로 보이지 않게 한다.
function HostDetail({ server, now }: { server: Server; now: number }) {
  const metric = (name: string, format: (value: number) => string, collectorName?: string) => {
    const collector = server.collectors.find((item) => item.name === collectorName)
    if (collector?.state === 'unsupported') return <span className="muted"><UnsupportedIcon /> 미지원</span>
    if (collector?.state === 'error') return <span className="collect-failed">수집 실패</span>
    const value = readMetric(server.metrics[name], now, format)
    return value.state === 'stale' ? `${value.text} (${value.age} 값)` : value.text
  }
  return (
    <>
      <header className="detail-head">
        <h2>{server.id}</h2>
        <p className="muted">서버</p>
      </header>
      <section className="detail-section">
        <Row label="도달 상태">{server.reachability.label}</Row>
        {server.reachability.detail && <p className="muted small">{server.reachability.detail}</p>}
        <Row label="마지막 수신">{server.last_seen ? `${formatAge(server.last_seen, now)}` : '기록 없음'}</Row>
        <Row label="가동 시간">{formatUptime(server.uptime_seconds)}</Row>
        <Row label="부팅 시각">{server.boot_time ? formatDateTime(server.boot_time) : '알 수 없음'}</Row>
        <Row label="Docker">{server.docker_ok ? '응답' : '응답 없음'}</Row>
        <Row label="시계 차이">{Math.abs(server.clock_skew_ms) > 2000 ? `${server.clock_skew_ms}ms (확인 필요)` : '2초 이내'}</Row>
      </section>
      <section className="detail-section">
        <h3>지표</h3>
        <Row label="CPU">{metric('host.cpu.percent', formatPercent, 'cpu')}</Row>
        <Row label="메모리">{metric('host.mem.percent', formatPercent, 'memory')}</Row>
        <Row label="디스크">{metric('host.disk.percent', formatPercent, 'disk')}</Row>
        <Row label="디스크 읽기">{metric('host.disk.read_bps', formatRate, 'disk_io')}</Row>
        <Row label="디스크 쓰기">{metric('host.disk.write_bps', formatRate, 'disk_io')}</Row>
        <Row label="네트워크 수신">{metric('host.net.rx_bps', formatRate, 'network')}</Row>
        <Row label="네트워크 송신">{metric('host.net.tx_bps', formatRate, 'network')}</Row>
        <Row label="온도">{metric('host.temperature', (v) => `${v.toFixed(0)}°C`, 'temperature')}</Row>
      </section>
      <section className="detail-section">
        <h3>수집기</h3>
        {server.collectors.length === 0 && <p className="muted">수집기 상태를 받지 못했습니다.</p>}
        {server.collectors.map((collector) => (
          <div key={collector.name}>
            <Row label={collector.name}>
              {collector.state === 'unsupported' && <UnsupportedIcon />} {collectorText(collector.state)}
            </Row>
            {collector.error && <p className="muted small">{collector.error}</p>}
          </div>
        ))}
      </section>
    </>
  )
}

function ContainerDetail({ container, now, historical }: { container: Container; now: number; historical: boolean }) {
  const metric = (name: string, format: (value: number) => string) => {
    const value = readMetric(container.metrics[name], now, format)
    return value.state === 'stale' ? `${value.text} (${value.age} 값)` : value.text
  }
  const running = container.state === 'running'
  return (
    <div className="container-detail">
      <div className="container-name mono">{container.name}</div>
      <Row label="ID"><span className="mono">{shortID(container.id)}</span></Row>
      <Row label="이미지"><span className="mono">{container.image}</span></Row>
      <Row label="실행">{container.state}{container.state === 'exited' ? ` (exit ${container.exit_code})` : ''}</Row>
      <Row label="헬스체크">{container.health === 'none' ? '설정 없음' : container.health}</Row>
      {container.oom_killed && <Row label="OOM">OOM 종료 기록 있음</Row>}
      <Row label="재시작">{container.restart_count}회 · 정책 {container.restart_policy || '없음'}</Row>
      <Row label="시작 시각">{formatDateTime(container.started_at)}</Row>
      {running && !historical && (
        <>
          <Row label="CPU">{metric('container.cpu.percent', formatPercent)}</Row>
          <Row label="메모리">
            {metric('container.mem.bytes', formatBytes)}
            {container.metrics['container.mem.limit_bytes'] && ` / ${formatBytes(container.metrics['container.mem.limit_bytes'].value)}`}
          </Row>
          <Row label="수신">{metric('container.net.rx_bps', formatRate)}</Row>
          <Row label="송신">{metric('container.net.tx_bps', formatRate)}</Row>
        </>
      )}
      {!running && <p className="muted small">실행 중이 아니어서 지표가 없습니다. 0으로 표시하지 않습니다.</p>}
      {(container.ports ?? []).length > 0 && (
        <Row label="포트">
          <span className="mono port-list">
            {container.ports!.map((port) => (
              <span key={`${port.host_ip}:${port.host_port}:${port.container_port}`}>
                {port.host_port ? `${port.host_ip || '0.0.0.0'}:${port.host_port} → ${port.container_port}/${port.proto}` : `${port.container_port}/${port.proto} (미공개)`}
              </span>
            ))}
          </span>
        </Row>
      )}
      {(container.networks ?? []).map((network) => (
        <Row key={network.network} label="네트워크">
          <button type="button" className="link mono" onClick={() => updateContext({ target: `net:${network.network}`, event: undefined })}>{network.network}</button>
          <span className="mono muted"> {network.ip}</span>
        </Row>
      ))}
    </div>
  )
}

function ServiceDetail({ service, topology, now }: { service: Service; topology: Topology; now: number }) {
  const declared = topology.relations.filter((relation) => relation.type === 'declared_depends_on' && relation.from === service.key)
  const dependents = topology.relations.filter((relation) => relation.type === 'declared_depends_on' && relation.to === service.key)
  return (
    <>
      <header className="detail-head">
        <h2>{service.name}</h2>
        <span className={`badge ${service.status}`}><StatusIcon status={service.status} size={12} />{STATUS_LABEL[service.status]}</span>
        <p className="muted">{projectLabel(service.project)} 프로젝트</p>
      </header>
      <section className="detail-section">
        <div className="headline">{serviceHeadline(service)}</div>
        {service.reasons.map((reason) => <p key={reason} className="small">{reason}</p>)}
        {service.response === 'historical' && <p className="muted small">과거 시점의 응답 상태는 흐름표 기록으로 확인합니다.</p>}
        <div className="detail-links">
          <button type="button" className="tool-button" onClick={() => updateContext({ tab: 'flow' })}>기록</button>
          <button type="button" className="tool-button" onClick={() => updateContext({ tab: 'metrics' })}>지표</button>
          <button type="button" className="tool-button" onClick={() => updateContext({ tab: 'logs' })}>로그</button>
        </div>
      </section>
      <section className="detail-section">
        <h3>응답 확인</h3>
        {service.probes.length === 0 && (
          <p className="muted small">{service.response === 'historical' ? '과거 시점에는 표시하지 않습니다.' : '응답 확인을 설정하지 않아 앱 응답 여부를 알 수 없습니다.'}</p>
        )}
        {service.probes.map((probe) => (
          <div key={probe.vantage} className="probe-row">
            <Row label={probe.vantage === 'internal' ? '내부' : '외부'}>
              {probe.state === 'ok' ? '응답' : probe.state === 'down' ? `무응답 (${probe.failures}회 연속)` : `실패 ${probe.failures}회`}
              {probe.latency_ms !== null ? ` · ${probe.latency_ms}ms` : ' · 소요 미측정'}
            </Row>
            <p className="muted small mono ellipsis" title={probe.target}>{probe.target} · {formatAge(probe.last_checked, now)}</p>
          </div>
        ))}
      </section>
      <section className="detail-section">
        <h3>컨테이너 {service.containers.length}개</h3>
        {service.containers.map((container) => (
          <ContainerDetail key={container.id} container={container} now={now} historical={topology.historical} />
        ))}
      </section>
      <section className="detail-section">
        <h3>선언된 의존</h3>
        {declared.length === 0 && dependents.length === 0 && <p className="muted small">선언된 의존이 없습니다.</p>}
        {declared.map((relation) => <Row key={relation.to} label="의존 대상"><span className="mono">{relation.to}</span></Row>)}
        {dependents.map((relation) => <Row key={relation.from} label="의존하는 쪽"><span className="mono">{relation.from}</span></Row>)}
        <p className="muted small">Compose 선언 기준입니다. 실제 호출 여부는 요청 추적이 연동되어야 알 수 있습니다.</p>
      </section>
    </>
  )
}

function NetworkDetail({ network }: { network: Network }) {
  return (
    <>
      <header className="detail-head">
        <h2 className="mono">{network.name}</h2>
        <p className="muted">Docker 네트워크</p>
      </header>
      <section className="detail-section">
        <Row label="드라이버"><span className="mono">{network.driver}</span></Row>
        <Row label="서브넷"><span className="mono">{network.subnet || '알 수 없음'}</span></Row>
        <Row label="내부 전용">{network.internal ? '예' : '아니요'}</Row>
      </section>
      <section className="detail-section">
        <h3>소속 서비스 {network.members.length}개</h3>
        {network.members.map((member) => (
          <div key={member}>
            <button type="button" className="link mono" onClick={() => updateContext({ target: `svc:${member}`, event: undefined })}>{member}</button>
          </div>
        ))}
        <p className="muted small">같은 네트워크에 속한다는 뜻입니다. 서로 호출한다는 뜻은 아닙니다.</p>
      </section>
    </>
  )
}

function ProjectDetail({ project }: { project: Project }) {
  return (
    <>
      <header className="detail-head">
        <h2>{projectLabel(project.key)}</h2>
        <span className={`badge ${project.status}`}><StatusIcon status={project.status} size={12} />{STATUS_LABEL[project.status]}</span>
        <p className="muted">{project.key === '_standalone' ? 'Compose 라벨이 없는 컨테이너' : 'Compose 프로젝트'}</p>
      </header>
      <section className="detail-section">
        <h3>서비스 {project.services.length}개</h3>
        {project.services.map((service) => (
          <Row key={service.key} label={service.name}>
            <button type="button" className="link" onClick={() => updateContext({ target: `svc:${service.key}`, event: undefined })}>{serviceHeadline(service)}</button>
          </Row>
        ))}
      </section>
    </>
  )
}

export function DetailPanel({ meta, server, topology, target, now, selectedEvent, events }: Props) {
  const parsed = parseTarget(target)
  let content: ReactNode = selectedEvent ? null : <p className="muted empty">맵이나 왼쪽 목록에서 대상을 선택하면 상세 정보가 표시됩니다.</p>

  if (parsed?.kind === 'host' && server) {
    content = <HostDetail server={server} now={now} />
  } else if (parsed?.kind === 'service' && topology) {
    const service = topology.projects.flatMap((project) => project.services).find((item) => item.key === parsed.key)
    content = service ? <ServiceDetail service={service} topology={topology} now={now} /> : <p className="muted empty">이 시점의 구성에 없는 서비스입니다.</p>
  } else if (parsed?.kind === 'network' && topology) {
    const network = topology.networks.find((item) => item.name === parsed.name)
    content = network ? <NetworkDetail network={network} /> : <p className="muted empty">이 시점의 구성에 없는 네트워크입니다.</p>
  } else if (parsed?.kind === 'project' && topology) {
    const project = topology.projects.find((item) => item.key === parsed.key)
    content = project ? <ProjectDetail project={project} /> : <p className="muted empty">이 시점의 구성에 없는 프로젝트입니다.</p>
  }

  return (
    <aside className="detail" aria-label="상세 정보">
      <h2 className="panel-title">상세</h2>
      {selectedEvent && <EventDetail event={selectedEvent} events={events} />}
      {content}
      {meta?.read_only && <ReadOnlyNotice />}
    </aside>
  )
}
