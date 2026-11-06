import { describe, expect, it } from 'vitest'
import type { EventRow, GapRow, Topology } from './api'
import { mergeRecords, targetServices } from './useFlowRecords'

const event = (id: number, occurred_at: string) => ({ id, occurred_at }) as EventRow
const gap = (to: string) => ({ source: 'agent', from: '2026-11-06T02:00:00Z', to, reason: '끊김', dropped: 0 }) as GapRow

describe('mergeRecords', () => {
  it('공백 구간을 시간순으로 끼워 넣는다', () => {
    const records = mergeRecords(
      [event(1, '2026-11-06T03:00:00Z'), event(2, '2026-11-06T01:00:00Z')],
      [gap('2026-11-06T02:30:00Z')],
    )
    expect(records.map((record) => record.kind)).toEqual(['event', 'gap', 'event'])
  })
})

describe('targetServices', () => {
  const topology = {
    projects: [{ key: 'blog', services: [{ key: 'blog/api' }, { key: 'blog/db' }] }],
    networks: [{ name: 'proxy', members: ['blog/api', 'shop/app'] }],
  } as unknown as Topology

  it('대상 종류에 맞는 서비스로 좁힌다', () => {
    expect(targetServices('svc:blog/api', topology)).toEqual(['blog/api'])
    expect(targetServices('project:blog', topology)).toEqual(['blog/api', 'blog/db'])
    expect(targetServices('net:proxy', topology)).toEqual(['blog/api', 'shop/app'])
  })

  it('서버 전체나 선택 없음은 필터를 걸지 않는다', () => {
    expect(targetServices('host', topology)).toBeUndefined()
    expect(targetServices(undefined, topology)).toBeUndefined()
  })
})
