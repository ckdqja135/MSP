import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ScreenInput } from './api'
import { captureInput, queueInput, releases, settle, wheelSteps, type Held } from './remoteInput'

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

describe('settle', () => {
  const held = (): Held => ({ keys: new Set(), buttons: new Set() })

  it('도착한 묶음은 누름을 넣고 뗌을 뺀다', () => {
    const state = held()
    settle(state, [{ type: 'keydown', code: 'ShiftLeft' }, { type: 'keydown', code: 'KeyA' }, { type: 'keyup', code: 'KeyA' }, { type: 'down', x: 0, y: 0, button: 1 }], true)
    expect([...state.keys]).toEqual(['ShiftLeft'])
    expect([...state.buttons]).toEqual([1])
  })

  it('도착을 확인하지 못한 묶음의 뗌은 가지 않은 것으로 본다', () => {
    const state = held()
    settle(state, [{ type: 'keydown', code: 'Backspace' }], true)
    settle(state, [{ type: 'keyup', code: 'Backspace' }, { type: 'keydown', code: 'KeyB' }], false)
    expect([...state.keys].sort()).toEqual(['Backspace', 'KeyB'])
    expect(releases(state, { x: 0.5, y: 0.5 })).toEqual([{ type: 'keyup', code: 'Backspace' }, { type: 'keyup', code: 'KeyB' }])
  })
})

// 브라우저 없이 입력 모으기를 돌리기 위한 최소한의 창과 그림이다.
class FakeTarget {
  listeners = new Map<string, ((event: unknown) => void)[]>()
  addEventListener(type: string, listener: (event: unknown) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }
  removeEventListener(type: string, listener: (event: unknown) => void) {
    this.listeners.set(type, (this.listeners.get(type) ?? []).filter((item) => item !== listener))
  }
  fire(type: string, event: object) {
    for (const listener of this.listeners.get(type) ?? []) listener({ preventDefault() {}, ...event })
  }
  getBoundingClientRect() {
    return { left: 0, top: 0, width: 100, height: 100 }
  }
}

describe('captureInput', () => {
  let page: FakeTarget
  beforeEach(() => {
    vi.useFakeTimers()
    page = new FakeTarget()
    vi.stubGlobal('window', page)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('뗌을 보내다 실패하면 도착할 때까지 다시 보내고 밀린 입력은 버린다', async () => {
    const sent: ScreenInput[][] = []
    let failing = false
    const send = async (inputs: ScreenInput[]) => {
      if (failing) throw new Error('409')
      sent.push(inputs)
    }
    const image = new FakeTarget()
    const stop = captureInput(image as unknown as HTMLElement, send)

    page.fire('keydown', { code: 'Backspace' })
    await vi.advanceTimersByTimeAsync(40)
    expect(sent).toEqual([[{ type: 'keydown', code: 'Backspace' }]])

    // 연결이 끊긴 사이에 키를 떼고 화면을 누른다.
    failing = true
    page.fire('keyup', { code: 'Backspace' })
    image.fire('mousedown', { button: 0, clientX: 10, clientY: 10 })
    await vi.advanceTimersByTimeAsync(40)
    failing = false
    await vi.advanceTimersByTimeAsync(600)

    // 끊긴 사이의 누름은 다시 들어가지 않고, 눌려 있을 수 있는 키와 버튼의 뗌만 간다.
    const after = sent.slice(1).flat()
    expect(after).toContainEqual({ type: 'keyup', code: 'Backspace' })
    expect(after.some((input) => input.type === 'down' || input.type === 'keydown')).toBe(false)
    expect(after.filter((input) => input.type === 'up')).toHaveLength(1)

    // 뗌이 도착한 뒤에는 더 보내지 않는다.
    const count = sent.length
    await vi.advanceTimersByTimeAsync(2000)
    expect(sent).toHaveLength(count)
    stop()
  })

  it('멈출 때 서버에 눌려 있는 키를 뗀다', async () => {
    const sent: ScreenInput[][] = []
    const stop = captureInput(new FakeTarget() as unknown as HTMLElement, async (inputs) => {
      sent.push(inputs)
    })
    page.fire('keydown', { code: 'ShiftLeft' })
    await vi.advanceTimersByTimeAsync(40)
    // 아직 보내지 않은 누름은 서버에 가지 않았으니 뗄 것도 없다.
    page.fire('keydown', { code: 'KeyA' })
    stop()
    await vi.advanceTimersByTimeAsync(10)
    expect(sent).toEqual([[{ type: 'keydown', code: 'ShiftLeft' }], [{ type: 'keyup', code: 'ShiftLeft' }]])
    expect(page.listeners.get('keydown')).toEqual([])
  })
})
