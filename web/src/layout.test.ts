import { describe, expect, it } from 'vitest'
import type { Network, Project, Service, Topology } from './api'
import { assignSlots, computeLayout, emptySlots } from './layout'

const service = (key: string): Service => ({
  key,
  name: key.split('/')[1],
  project: key.split('/')[0],
  status: 'ok',
  runtime: 'running',
  response: 'unconfirmed',
  reasons: [],
  containers: [],
  probes: [],
})

const project = (key: string, ...names: string[]): Project => ({
  key,
  status: 'ok',
  services: names.map((name) => service(`${key}/${name}`)),
})

const topology = (projects: Project[], networks: Network[] = []): Topology => ({
  server_id: 's',
  version_id: 1,
  valid_from: '',
  valid_to: null,
  observed_at: '',
  historical: false,
  projects,
  networks,
  relations: [],
})

describe('노드 위치 안정성', () => {
  const none = new Set<string>()

  it('서비스가 추가되어도 기존 노드는 움직이지 않는다', () => {
    const before = topology([project('blog', 'api', 'db'), project('shop', 'app')])
    const slotsBefore = assignSlots(emptySlots(), before)
    const layoutBefore = computeLayout(before, slotsBefore, none)

    // 이름순으로 api 앞에 오는 서비스와 blog 앞에 오는 프로젝트를 추가한다.
    const after = topology([project('auth', 'server'), project('blog', 'admin', 'api', 'db'), project('shop', 'app')])
    const slotsAfter = assignSlots(slotsBefore, after)
    const layoutAfter = computeLayout(after, slotsAfter, none)

    for (const key of ['blog/api', 'blog/db', 'shop/app']) {
      expect(layoutAfter.services[key]).toEqual(layoutBefore.services[key])
    }
    expect(layoutAfter.services['blog/admin']).toBeDefined()
  })

  it('서비스가 사라져도 다른 노드는 움직이지 않고 자리는 비워 둔다', () => {
    const before = topology([project('blog', 'api', 'db', 'web')])
    const slots = assignSlots(emptySlots(), before)
    const layoutBefore = computeLayout(before, slots, none)

    const after = topology([project('blog', 'api', 'web')])
    const layoutAfter = computeLayout(after, assignSlots(slots, after), none)
    expect(layoutAfter.services['blog/web']).toEqual(layoutBefore.services['blog/web'])
  })

  it('같은 입력은 같은 배치를 만든다', () => {
    const data = topology([project('blog', 'api')])
    const slots = assignSlots(emptySlots(), data)
    expect(computeLayout(data, slots, none)).toEqual(computeLayout(data, assignSlots(slots, data), none))
  })

  it('미분류 컨테이너는 처음 배정할 때 맨 뒤에 둔다', () => {
    const data = topology([project('_standalone', 'minio'), project('blog', 'api')])
    expect(assignSlots(emptySlots(), data).projects).toEqual(['blog', '_standalone'])
  })

  it('그룹을 접으면 다른 프로젝트 노드는 움직이지 않는다', () => {
    const data = topology([project('blog', 'api', 'db'), project('shop', 'app')])
    const slots = assignSlots(emptySlots(), data)
    const open = computeLayout(data, slots, none)
    const closed = computeLayout(data, slots, new Set(['blog']))
    expect(closed.services['shop/app']).toEqual(open.services['shop/app'])
    expect(closed.services['blog/api']).toBeUndefined()
  })
})
