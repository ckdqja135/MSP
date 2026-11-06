import { useState } from 'react'
import type { Server, Topology } from '../api'
import { updateContext } from '../context'
import { HOST_EVIDENCE_TAG, STATUS_LABEL, hostServiceKey, hostServiceName, projectLabel } from '../status'
import { IsoNetwork, StatusIcon } from './Icons'

interface Props {
  server?: Server
  topology?: Topology
  target?: string
}

export function Explorer({ server, topology, target }: Props) {
  const [query, setQuery] = useState('')
  const needle = query.trim().toLowerCase()
  const select = (value: string) => updateContext({ target: value, event: undefined })

  return (
    <nav className="explorer" aria-label="프로젝트 탐색">
      <h2 className="panel-title">탐색</h2>
      <input
        className="search"
        type="search"
        placeholder="서비스, 컨테이너 검색"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />
      <div className="tree">
        {server && (
          <button type="button" className={`tree-item host ${target === 'host' ? 'selected' : ''}`} onClick={() => select('host')}>
            <span className="grow">{server.id}</span>
            <span className="muted">서버</span>
          </button>
        )}
        {topology?.projects.map((project) => {
          const services = project.services.filter(
            (service) =>
              !needle ||
              service.key.toLowerCase().includes(needle) ||
              service.containers.some((container) => container.name.toLowerCase().includes(needle)),
          )
          if (needle && services.length === 0) return null
          const projectID = `project:${project.key}`
          return (
            <div key={project.key} className="tree-group">
              <button type="button" className={`tree-item project ${target === projectID ? 'selected' : ''}`} onClick={() => select(projectID)}>
                <StatusIcon status={project.status} />
                <span className="grow">{projectLabel(project.key)}</span>
                <span className="sr-only">{STATUS_LABEL[project.status]}</span>
                <span className="muted mono">{project.services.length}</span>
              </button>
              {services.map((service) => {
                const id = `svc:${service.key}`
                return (
                  <button key={service.key} type="button" className={`tree-item service ${target === id ? 'selected' : ''}`} onClick={() => select(id)}>
                    <StatusIcon status={service.status} size={12} />
                    <span className="grow ellipsis">{service.name}</span>
                    <span className={`tree-status ${service.status}`}>{STATUS_LABEL[service.status]}</span>
                  </button>
                )
              })}
            </div>
          )
        })}
        {(topology?.host_services ?? []).length > 0 && !needle && (
          <div className="tree-group">
            <div className="tree-heading">호스트 서비스 (Docker 밖)</div>
            {topology!.host_services!.map((service) => {
              const id = `hostsvc:${hostServiceKey(service)}`
              return (
                <button key={id} type="button" className={`tree-item service ${target === id ? 'selected' : ''}`} onClick={() => select(id)}>
                  <StatusIcon status={service.listening ? 'ok' : 'critical'} size={12} />
                  <span className="grow ellipsis">{hostServiceName(service)}</span>
                  <span className="muted tree-status">{HOST_EVIDENCE_TAG[service.evidence]}</span>
                </button>
              )
            })}
          </div>
        )}
        {topology && topology.networks.length > 0 && !needle && (
          <div className="tree-group">
            <div className="tree-heading">네트워크</div>
            {topology.networks.map((network) => {
              const id = `net:${network.name}`
              return (
                <button key={network.name} type="button" className={`tree-item service ${target === id ? 'selected' : ''}`} onClick={() => select(id)}>
                  <IsoNetwork size={13} />
                  <span className="grow ellipsis mono">{network.name}</span>
                  <span className="muted mono">{network.members.length}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    </nav>
  )
}
