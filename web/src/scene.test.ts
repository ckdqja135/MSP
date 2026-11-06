import { describe, expect, it } from 'vitest'
import type { Container, Network, Project, Relation, Service, Topology } from './api'
import { assignSlots, computeScene, emptySlots, isFinishedJob, routeBetween, serviceRole } from './scene'

const container = (overrides: Partial<Container> = {}): Container =>
  ({ id: 'c', name: 'c', image: 'app:1', state: 'running', exit_code: 0, health: 'none', restart_count: 0, created_at: '', started_at: '', replica: 1, project: '', service: '', metrics: {}, ...overrides }) as Container

const service = (key: string, containers: Container[] = [container()]): Service => ({
  key,
  name: key.split('/')[1],
  project: key.split('/')[0],
  status: 'ok',
  runtime: 'running',
  response: 'unconfirmed',
  reasons: [],
  containers,
  probes: [],
})

const project = (key: string, ...names: string[]): Project => ({
  key,
  status: 'ok',
  services: names.map((name) => service(`${key}/${name}`)),
})

const network = (name: string, members: string[], shared = false): Network => ({ id: name, name, driver: 'bridge', members, shared })

const topology = (projects: Project[], networks: Network[] = [], relations: Relation[] = []): Topology => ({
  server_id: 's',
  version_id: 1,
  valid_from: '',
  valid_to: null,
  observed_at: '',
  historical: false,
  projects,
  networks,
  relations,
})

const positions = (data: Topology, slots = assignSlots(emptySlots(), data), collapsed = new Set<string>()) =>
  Object.fromEntries(computeScene(data, slots, collapsed).devices.map((device) => [device.id, device.point]))

describe('장비 위치 안정성', () => {
  it('서비스나 프로젝트가 추가되어도 기존 장비는 움직이지 않는다', () => {
    const before = topology([project('blog', 'api', 'db'), project('shop', 'app')])
    const slots = assignSlots(emptySlots(), before)
    const placedBefore = positions(before, slots)

    // 이름순으로 앞에 오는 서비스와 프로젝트를 추가한다.
    const after = topology([project('auth', 'server'), project('blog', 'admin', 'api', 'db'), project('shop', 'app')])
    const placedAfter = positions(after, assignSlots(slots, after))

    for (const id of ['svc:blog/api', 'svc:blog/db', 'svc:shop/app', 'host']) {
      expect(placedAfter[id]).toEqual(placedBefore[id])
    }
    expect(placedAfter['svc:blog/admin']).toBeDefined()
    expect(placedAfter['svc:auth/server']).toBeDefined()
  })

  it('서비스가 사라져도 다른 장비는 움직이지 않는다', () => {
    const before = topology([project('blog', 'api', 'db', 'web')])
    const slots = assignSlots(emptySlots(), before)
    const after = topology([project('blog', 'api', 'web')])
    expect(positions(after, assignSlots(slots, after))['svc:blog/web']).toEqual(positions(before, slots)['svc:blog/web'])
  })

  it('컨테이너가 재생성되어도 서비스 키가 같으면 같은 자리다', () => {
    const before = topology([{ key: 'blog', status: 'ok', services: [service('blog/api', [container({ id: 'old' })])] }])
    const slots = assignSlots(emptySlots(), before)
    const after = topology([{ key: 'blog', status: 'ok', services: [service('blog/api', [container({ id: 'new' })])] }])
    expect(positions(after, assignSlots(slots, after))).toEqual(positions(before, slots))
  })

  it('그룹을 접어도 다른 프로젝트 장비는 움직이지 않는다', () => {
    const data = topology([project('blog', 'api', 'db'), project('shop', 'app')])
    const slots = assignSlots(emptySlots(), data)
    const closed = positions(data, slots, new Set(['blog']))
    expect(closed['svc:shop/app']).toEqual(positions(data, slots)['svc:shop/app'])
    expect(closed['svc:blog/api']).toBeUndefined()
  })

  it('미분류 컨테이너는 처음 배정할 때 맨 뒤에 둔다', () => {
    const data = topology([project('_standalone', 'minio'), project('blog', 'api')])
    expect(assignSlots(emptySlots(), data).projects).toEqual(['blog', '_standalone'])
  })
})

describe('연결선의 근거', () => {
  const data = topology(
    [project('blog', 'api', 'db'), project('shop', 'app')],
    [network('blog_default', ['blog/api', 'blog/db']), network('proxy', ['blog/api', 'shop/app'], true)],
  )
  const scene = computeScene(data, assignSlots(emptySlots(), data), new Set())

  it('같은 네트워크에 속해도 서비스끼리 잇지 않는다', () => {
    for (const link of scene.links) {
      expect(link.from.startsWith('svc:') && link.to.startsWith('svc:')).toBe(false)
    }
    const members = scene.links.filter((link) => link.kind === 'member' && link.to.startsWith('svc:'))
    expect(members).toHaveLength(4)
    expect(members.every((link) => link.from.startsWith('net:'))).toBe(true)
  })

  it('선언된 의존만 서비스끼리 잇는다', () => {
    const declared = topology([project('blog', 'api', 'db')], [], [{ type: 'declared_depends_on', from: 'blog/api', to: 'blog/db', evidence: 'compose-label' }])
    const links = computeScene(declared, assignSlots(emptySlots(), declared), new Set()).links
    expect(links.filter((link) => link.from.startsWith('svc:')).map((link) => link.kind)).toEqual(['declared'])
  })

  it('공개 포트와 루프백 바인딩을 구분하고 공개 포트가 없으면 외부를 그리지 않는다', () => {
    const open = service('blog/web', [container({ ports: [{ host_ip: '0.0.0.0', host_port: 443, container_port: 443, proto: 'tcp' }] })])
    const local = service('blog/api', [container({ ports: [{ host_ip: '127.0.0.1', host_port: 18080, container_port: 8080, proto: 'tcp' }] })])
    const closed = service('blog/db', [container({ ports: [{ container_port: 5432, proto: 'tcp' }] })])

    const onlyLocal = topology([{ key: 'blog', status: 'ok', services: [local, closed] }])
    const localScene = computeScene(onlyLocal, assignSlots(emptySlots(), onlyLocal), new Set())
    expect(localScene.devices.some((device) => device.kind === 'internet')).toBe(false)
    expect(localScene.links.map((link) => link.kind)).toEqual(['loopback'])

    const withOpen = topology([{ key: 'blog', status: 'ok', services: [open, local, closed] }])
    const openScene = computeScene(withOpen, assignSlots(emptySlots(), withOpen), new Set())
    expect(openScene.devices.some((device) => device.kind === 'internet')).toBe(true)
    expect(openScene.links.map((link) => link.kind).sort()).toEqual(['external', 'loopback', 'publish'])
    expect(openScene.links.find((link) => link.kind === 'external')?.label).toBe('포트 443')
  })

  it('실제 연결 경로는 양쪽 장비가 있을 때만 만든다', () => {
    expect(routeBetween(scene, 'host', 'svc:blog/api')).toMatch(/^M/)
    expect(routeBetween(scene, 'host', 'svc:none/none')).toBeUndefined()
  })
})

describe('끝난 일회성 작업', () => {
  it('구조도에는 그리지 않고 건수를 남긴다', () => {
    const job = service('blog/migrate', [container({ one_off: true, state: 'exited' })])
    const data = topology([{ key: 'blog', status: 'ok', services: [service('blog/api'), job] }])
    const scene = computeScene(data, assignSlots(emptySlots(), data), new Set())
    expect(isFinishedJob(job)).toBe(true)
    expect(scene.devices.some((device) => device.id === 'svc:blog/migrate')).toBe(false)
    expect(scene.plates[0].hiddenJobs).toBe(1)
  })

  it('실행 중인 일회성 작업은 그린다', () => {
    const job = service('blog/migrate', [container({ one_off: true, state: 'running' })])
    expect(isFinishedJob(job)).toBe(false)
  })
})

describe('serviceRole', () => {
  it('이미지와 이름으로 아이콘 모양을 고른다', () => {
    const of = (key: string, image: string) => serviceRole(service(key, [container({ image })]))
    expect(of('a/db', 'postgres:16')).toBe('database')
    expect(of('a/c', 'redis:7')).toBe('cache')
    expect(of('a/p', 'caddy:2')).toBe('proxy')
    expect(of('a/s', 'minio/minio')).toBe('storage')
    expect(of('a/web', 'registry.test/blog-web:1')).toBe('web')
    expect(of('a/api', 'registry.test/blog-api:1')).toBe('app')
  })
})
