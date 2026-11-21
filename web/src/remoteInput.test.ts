import { describe, expect, it } from 'vitest'
import type { ScreenInput } from './api'
import { queueInput, wheelSteps } from './remoteInput'

describe('queueInput', () => {
  it('이어진 이동은 마지막 위치만 남긴다', () => {
    const queue: ScreenInput[] = []
    queueInput(queue, { type: 'move', x: 0.1, y: 0.1 })
    queueInput(queue, { type: 'move', x: 0.2, y: 0.2 })
    queueInput(queue, { type: 'move', x: 0.3, y: 0.3 })
    expect(queue).toEqual([{ type: 'move', x: 0.3, y: 0.3 }])
  })

  it('누름과 뗌 사이의 이동은 순서를 지킨다', () => {
    const queue: ScreenInput[] = []
    queueInput(queue, { type: 'move', x: 0.1, y: 0.1 })
    queueInput(queue, { type: 'down', x: 0.1, y: 0.1, button: 1 })
    queueInput(queue, { type: 'move', x: 0.5, y: 0.5 })
    queueInput(queue, { type: 'up', x: 0.5, y: 0.5, button: 1 })
    expect(queue.map((input) => input.type)).toEqual(['move', 'down', 'move', 'up'])
  })

  it('누름과 뗌은 같은 키라도 버리지 않는다', () => {
    const queue: ScreenInput[] = []
    for (let i = 0; i < 3; i++) {
      queueInput(queue, { type: 'keydown', code: 'KeyA' })
      queueInput(queue, { type: 'keyup', code: 'KeyA' })
    }
    expect(queue).toHaveLength(6)
  })

  it('이어진 휠은 합치되 서버가 받는 범위를 넘기지 않는다', () => {
    const queue: ScreenInput[] = []
    queueInput(queue, { type: 'wheel', x: 0.5, y: 0.5, dy: 6 })
    queueInput(queue, { type: 'wheel', x: 0.5, y: 0.5, dy: 3 })
    expect(queue).toEqual([{ type: 'wheel', x: 0.5, y: 0.5, dx: 0, dy: 9 }])
    queueInput(queue, { type: 'wheel', x: 0.5, y: 0.5, dy: 4 })
    expect(queue).toHaveLength(2)
    // 반대로 굴려 0이 되는 휠은 서버가 받지 않으므로 합치지 않는다.
    queueInput(queue, { type: 'wheel', x: 0.5, y: 0.5, dy: -4 })
    expect(queue).toHaveLength(3)
  })
})

describe('wheelSteps', () => {
  it('작은 이동은 모았다가 한 칸이 되면 보낸다', () => {
    expect(wheelSteps(20)).toEqual({ steps: 0, rest: 20 })
    expect(wheelSteps(70)).toEqual({ steps: 1, rest: 10 })
    expect(wheelSteps(-130)).toEqual({ steps: -2, rest: -10 })
  })

  it('한 번에 보내는 칸 수에 상한을 둔다', () => {
    expect(wheelSteps(100_000).steps).toBe(10)
    expect(wheelSteps(-100_000).steps).toBe(-10)
  })
})
