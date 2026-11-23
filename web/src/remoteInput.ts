import type { ScreenInput } from './api'

// 브라우저의 마우스 버튼 번호를 서버의 버튼 번호로 바꾼다. 왼쪽, 가운데, 오른쪽이다.
const BUTTONS: Record<number, number> = { 0: 1, 1: 2, 2: 3 }
// 휠을 이만큼 굴리면 한 칸으로 친다. 트랙패드는 작은 값을 여러 번 보낸다.
const WHEEL_STEP = 60
const MAX_WHEEL = 10

const clamp = (value: number) => Math.min(1, Math.max(0, value))

// queueInput은 보낼 입력을 줄에 넣는다. 이어진 이동은 마지막 위치만 남기고, 이어진 휠은 합친다.
// 누름과 뗌은 하나도 버리지 않는다. 뗌이 빠지면 서버에서 키가 눌린 채로 남는다.
export function queueInput(queue: ScreenInput[], input: ScreenInput): void {
  const last = queue[queue.length - 1]
  if (input.type === 'move' && last?.type === 'move') {
    queue[queue.length - 1] = input
    return
  }
  if (input.type === 'wheel' && last?.type === 'wheel') {
    const dx = (last.dx ?? 0) + (input.dx ?? 0)
    const dy = (last.dy ?? 0) + (input.dy ?? 0)
    if (Math.abs(dx) <= MAX_WHEEL && Math.abs(dy) <= MAX_WHEEL && (dx !== 0 || dy !== 0)) {
      queue[queue.length - 1] = { ...input, dx, dy }
      return
    }
  }
  queue.push(input)
}

// wheelSteps는 쌓인 휠 이동량에서 보낼 칸 수를 떼어 낸다. 남은 양은 다음에 이어서 센다.
export function wheelSteps(pending: number): { steps: number; rest: number } {
  const steps = Math.trunc(pending / WHEEL_STEP)
  return { steps: Math.max(-MAX_WHEEL, Math.min(MAX_WHEEL, steps)), rest: pending - steps * WHEEL_STEP }
}

// Held는 서버에서 눌려 있을 수 있는 키와 버튼이다. 누름을 보낸 때부터 뗌이 도착한 것을 확인할 때까지 들어 있다.
export interface Held {
  keys: Set<string>
  buttons: Set<number>
}

// settle은 보낸 묶음의 결과를 반영한다. 도착했으면 순서대로 누름은 넣고 뗌은 뺀다.
// 도착을 확인하지 못했으면 누름만 넣는다. 일부는 처리됐을 수 있고, 뗌은 가지 않았다고 봐야 하기 때문이다.
export function settle(held: Held, batch: ScreenInput[], delivered: boolean): void {
  for (const input of batch) {
    if (input.type === 'keydown' && input.code) held.keys.add(input.code)
    else if (input.type === 'down' && input.button) held.buttons.add(input.button)
    else if (delivered && input.type === 'keyup' && input.code) held.keys.delete(input.code)
    else if (delivered && input.type === 'up' && input.button) held.buttons.delete(input.button)
  }
}

// releases는 서버에서 눌려 있을 수 있는 키와 버튼을 모두 떼는 입력을 만든다.
export function releases(held: Held, point: { x: number; y: number }): ScreenInput[] {
  return [
    ...[...held.keys].map((code): ScreenInput => ({ type: 'keyup', code })),
    ...[...held.buttons].map((button): ScreenInput => ({ type: 'up', ...point, button })),
  ]
}

const BATCH = 200
// 전달에 실패하면 이만큼 쉬었다가 다시 보낸다.
const RETRY_MS = 500

// captureInput은 화면 그림 위의 마우스와 창 전체의 키보드 입력을 모아 send로 넘긴다.
// 돌려주는 함수를 부르면 눌린 채 남은 키와 버튼을 떼고 멈춘다.
export function captureInput(image: HTMLElement, send: (inputs: ScreenInput[]) => Promise<void>): () => void {
  const queue: ScreenInput[] = []
  const keys = new Set<string>()
  const buttons = new Set<number>()
  const remote: Held = { keys: new Set(), buttons: new Set() }
  let point = { x: 0.5, y: 0.5 }
  let wheelX = 0
  let wheelY = 0
  let sending = false
  let retryAt = 0
  let current: Promise<void> = Promise.resolve()

  // 순서가 바뀌지 않게 한 번에 하나씩 보낸다.
  const flush = async () => {
    if (queue.length === 0 || Date.now() < retryAt) return
    sending = true
    const batch = queue.splice(0, BATCH)
    try {
      await send(batch)
      settle(remote, batch, true)
    } catch {
      // 조작이 꺼졌거나 연결이 끊긴 경우다. 밀린 입력은 버린다. 뒤늦게 들어가면 엉뚱한 곳을 누르게 된다.
      // 대신 서버에 눌려 있을 수 있는 키와 버튼은 뗌이 도착할 때까지 다시 보낸다. 뗌이 빠지면 서버에서 키가 계속 입력된다.
      settle(remote, batch, false)
      queue.length = 0
      keys.clear()
      buttons.clear()
      queue.push(...releases(remote, point))
      retryAt = Date.now() + RETRY_MS
    } finally {
      sending = false
    }
  }
  const timer = setInterval(() => {
    if (!sending) current = flush()
  }, 30)

  const locate = (event: MouseEvent) => {
    const box = image.getBoundingClientRect()
    point = { x: clamp((event.clientX - box.left) / box.width), y: clamp((event.clientY - box.top) / box.height) }
    return point
  }
  const releaseKeys = () => {
    for (const code of keys) queueInput(queue, { type: 'keyup', code })
    keys.clear()
  }
  const releaseAll = () => {
    releaseKeys()
    for (const button of buttons) queueInput(queue, { type: 'up', ...point, button })
    buttons.clear()
  }

  const onMove = (event: MouseEvent) => queueInput(queue, { type: 'move', ...locate(event) })
  const onDown = (event: MouseEvent) => {
    event.preventDefault()
    const button = BUTTONS[event.button]
    if (!button) return
    buttons.add(button)
    queueInput(queue, { type: 'down', ...locate(event), button })
  }
  // 그림 밖에서 떼도 받아야 하므로 창 전체에서 듣는다.
  const onUp = (event: MouseEvent) => {
    const button = BUTTONS[event.button]
    if (!button || !buttons.delete(button)) return
    queueInput(queue, { type: 'up', ...locate(event), button })
  }
  const onWheel = (event: WheelEvent) => {
    event.preventDefault()
    const horizontal = wheelSteps(wheelX + event.deltaX)
    const vertical = wheelSteps(wheelY + event.deltaY)
    wheelX = horizontal.rest
    wheelY = vertical.rest
    if (horizontal.steps !== 0 || vertical.steps !== 0) {
      queueInput(queue, { type: 'wheel', ...locate(event), dx: horizontal.steps, dy: vertical.steps })
    }
  }
  const onMenu = (event: Event) => event.preventDefault()
  const onKeyDown = (event: KeyboardEvent) => {
    event.preventDefault()
    // 누르고 있을 때의 반복은 서버가 스스로 만든다.
    if (event.repeat || !event.code) return
    keys.add(event.code)
    queueInput(queue, { type: 'keydown', code: event.code })
  }
  const onKeyUp = (event: KeyboardEvent) => {
    event.preventDefault()
    // macOS는 Command를 누른 동안 다른 키의 뗌을 알려 주지 않는다. Command를 뗄 때 모두 뗀다.
    if (event.code.startsWith('Meta')) {
      releaseKeys()
      return
    }
    if (keys.delete(event.code)) queueInput(queue, { type: 'keyup', code: event.code })
  }

  image.addEventListener('mousemove', onMove)
  image.addEventListener('mousedown', onDown)
  image.addEventListener('wheel', onWheel, { passive: false })
  image.addEventListener('contextmenu', onMenu)
  window.addEventListener('mouseup', onUp)
  window.addEventListener('keydown', onKeyDown, true)
  window.addEventListener('keyup', onKeyUp, true)
  window.addEventListener('blur', releaseAll)

  return () => {
    clearInterval(timer)
    image.removeEventListener('mousemove', onMove)
    image.removeEventListener('mousedown', onDown)
    image.removeEventListener('wheel', onWheel)
    image.removeEventListener('contextmenu', onMenu)
    window.removeEventListener('mouseup', onUp)
    window.removeEventListener('keydown', onKeyDown, true)
    window.removeEventListener('keyup', onKeyUp, true)
    window.removeEventListener('blur', releaseAll)
    // 보내는 중인 묶음이 끝난 뒤에 뗀다. 앞지르면 뒤늦게 도착한 누름이 눌린 채로 남는다.
    // 아직 보내지 않은 입력은 버리고, 서버에 눌려 있을 수 있는 것만 뗀다.
    void current
      .then(() => {
        queue.length = 0
        const last = releases(remote, point)
        return last.length > 0 ? send(last) : undefined
      })
      .catch(() => undefined)
  }
}
