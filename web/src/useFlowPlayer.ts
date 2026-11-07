import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { buildTimeline, frameAt, hopProgress, nextArrival, overviewFrame, previousStepStart, type FlowFrame, type FlowScript, type Timeline } from './flow'

// waiting은 실시간 모드에서 도착한 요청을 모두 보여주고 다음 요청을 기다리는 상태다.
export type PlayStatus = 'idle' | 'playing' | 'paused' | 'done' | 'waiting'
export const SPEEDS = [0.5, 1, 2]
// 실시간 모드에서 이보다 많이 밀리면 최근 요청으로 건너뛴다. 실시간이 점점 늦어지지 않게 하기 위해서다.
const MAX_LIVE_LAG_MS = 15_000

export interface FlowPlayer {
  script?: FlowScript
  timeline?: Timeline
  status: PlayStatus
  speed: number
  frame?: FlowFrame
  progress: () => number
  toggle: () => void
  step: () => void
  previous: () => void
  reset: () => void
  cycleSpeed: () => void
  seekPacket: (packet: number) => void
  live: boolean
  // 밀려서 건너뛴 요청 수다.
  skipped: number
  startLive: (script: FlowScript) => void
  extendLive: (script: FlowScript, dropped: number) => void
}

const sameFrame = (a: FlowFrame | undefined, b: FlowFrame) =>
  a !== undefined && a.phase === b.phase && a.step === b.step && a.packet === b.packet && a.hop === b.hop && a.delivered === b.delivered

// useFlowPlayer는 재생 시각을 관리한다. 시각은 매 프레임 바뀌므로 ref에 두고, 화면 상태는 구간이 바뀔 때만 갱신한다.
export function useFlowPlayer(latest: FlowScript | undefined): FlowPlayer {
  const [frozen, setFrozen] = useState<FlowScript | undefined>()
  const [status, setStatus] = useState<PlayStatus>('idle')
  const [speed, setSpeed] = useState(1)
  const [frame, setFrame] = useState<FlowFrame | undefined>()
  const [live, setLive] = useState(false)
  const [skipped, setSkipped] = useState(0)
  const liveRef = useRef(false)
  const statusRef = useRef<PlayStatus>('idle')
  statusRef.current = status
  const script = frozen ?? latest
  const timeline = useMemo(() => (script ? buildTimeline(script) : undefined), [script])

  const clock = useRef(0)
  const stopAt = useRef<number | null>(null)
  const timelineRef = useRef(timeline)
  timelineRef.current = timeline
  const speedRef = useRef(speed)
  speedRef.current = speed

  const publish = useCallback((next: FlowFrame) => setFrame((current) => (sameFrame(current, next) ? current : next)), [])

  useEffect(() => {
    if (status !== 'playing') return
    let last = performance.now()
    let raf = 0
    const tick = (now: number) => {
      const active = timelineRef.current
      if (!active) return
      clock.current += (now - last) * speedRef.current
      last = now
      if (stopAt.current !== null && clock.current >= stopAt.current) {
        clock.current = stopAt.current
        stopAt.current = null
        publish(frameAt(active, clock.current))
        setStatus(clock.current >= active.total ? 'done' : 'paused')
        return
      }
      if (clock.current >= active.total) {
        clock.current = active.total
        publish(frameAt(active, clock.current))
        setStatus(liveRef.current ? 'waiting' : 'done')
        return
      }
      publish(frameAt(active, clock.current))
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [status, publish])

  // 재생을 시작할 때 대본을 고정한다. 데이터 갱신으로 재생 중인 내용이 바뀌면 안 된다.
  const begin = useCallback(() => {
    if (!latest) return undefined
    const fixed = buildTimeline(latest)
    setFrozen(latest)
    timelineRef.current = fixed
    clock.current = 0
    stopAt.current = null
    publish(frameAt(fixed, 0))
    return fixed
  }, [latest, publish])

  const toggle = useCallback(() => {
    if (status === 'playing' || status === 'waiting') {
      setStatus('paused')
    } else if (status === 'paused') {
      const active = timelineRef.current
      setStatus(liveRef.current && active && clock.current >= active.total ? 'waiting' : 'playing')
    } else if (begin()) {
      setStatus('playing')
    }
  }, [status, begin])

  const step = useCallback(() => {
    const active = status === 'idle' || status === 'done' ? begin() : timelineRef.current
    if (!active) return
    stopAt.current = nextArrival(active, clock.current)
    setStatus('playing')
  }, [status, begin])

  const previous = useCallback(() => {
    const active = timelineRef.current
    if (!active || status === 'idle') return
    clock.current = previousStepStart(active, clock.current)
    stopAt.current = null
    publish(frameAt(active, clock.current))
    if (status === 'done') setStatus('paused')
  }, [status, publish])

  const reset = useCallback(() => {
    clock.current = 0
    stopAt.current = null
    liveRef.current = false
    setLive(false)
    setSkipped(0)
    setFrozen(undefined)
    setFrame(undefined)
    setStatus('idle')
  }, [])

  // extendLive는 새 요청을 시간표 뒤에 붙인다. 앞에서 뺀 단계만큼 시각을 당겨 보던 곳을 그대로 이어 간다.
  const extendLive = useCallback((next: FlowScript, dropped: number) => {
    const previous = timelineRef.current
    const fixed = buildTimeline(next)
    if (previous && dropped > 0) clock.current = Math.max(0, clock.current - (previous.stepStarts[dropped] ?? previous.total))
    // 일시정지 중에는 건너뛰지 않는다. 멈춘 화면이 바뀌면 안 된다.
    const catchUp = fixed.stepStarts.findIndex((start) => fixed.total - start <= MAX_LIVE_LAG_MS)
    if (statusRef.current !== 'paused' && catchUp > 0 && clock.current < fixed.stepStarts[catchUp]) {
      const passed = fixed.stepStarts.filter((start) => start <= clock.current).length
      setSkipped((count) => count + catchUp - Math.max(passed - 1, 0))
      clock.current = fixed.stepStarts[catchUp]
    }
    liveRef.current = true
    stopAt.current = null
    setFrozen(next)
    timelineRef.current = fixed
    publish(frameAt(fixed, clock.current))
    setStatus((current) => (current === 'paused' ? 'paused' : clock.current < fixed.total ? 'playing' : 'waiting'))
  }, [publish])

  const startLive = useCallback((next: FlowScript) => {
    clock.current = 0
    setSkipped(0)
    setLive(true)
    extendLive(next, 0)
  }, [extendLive])

  const cycleSpeed = useCallback(() => setSpeed((current) => SPEEDS[(SPEEDS.indexOf(current) + 1) % SPEEDS.length]), [])

  const seekPacket = useCallback((packet: number) => {
    const active = status === 'idle' ? begin() : timelineRef.current
    if (!active) return
    clock.current = active.arrivals[packet] ?? 0
    stopAt.current = null
    publish(frameAt(active, clock.current))
    setStatus('paused')
  }, [status, begin, publish])

  const progress = useCallback(() => {
    const active = timelineRef.current
    return active ? hopProgress(active, clock.current) : 0
  }, [])

  return {
    script,
    timeline,
    status,
    speed,
    frame: status === 'idle' ? (timeline ? overviewFrame(timeline) : undefined) : frame,
    progress,
    toggle,
    step,
    previous,
    reset,
    cycleSpeed,
    seekPacket,
    live,
    skipped,
    startLive,
    extendLive,
  }
}
