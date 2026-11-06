import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { buildTimeline, frameAt, hopProgress, nextArrival, overviewFrame, previousStepStart, type FlowFrame, type FlowScript, type Timeline } from './flow'

export type PlayStatus = 'idle' | 'playing' | 'paused' | 'done'
export const SPEEDS = [0.5, 1, 2]

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
}

const sameFrame = (a: FlowFrame | undefined, b: FlowFrame) =>
  a !== undefined && a.phase === b.phase && a.step === b.step && a.packet === b.packet && a.hop === b.hop && a.delivered === b.delivered

// useFlowPlayer는 재생 시각을 관리한다. 시각은 매 프레임 바뀌므로 ref에 두고, 화면 상태는 구간이 바뀔 때만 갱신한다.
export function useFlowPlayer(latest: FlowScript | undefined): FlowPlayer {
  const [frozen, setFrozen] = useState<FlowScript | undefined>()
  const [status, setStatus] = useState<PlayStatus>('idle')
  const [speed, setSpeed] = useState(1)
  const [frame, setFrame] = useState<FlowFrame | undefined>()
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
        setStatus('done')
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
    if (status === 'playing') {
      setStatus('paused')
    } else if (status === 'paused') {
      setStatus('playing')
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
    setFrozen(undefined)
    setFrame(undefined)
    setStatus('idle')
  }, [])

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
  }
}
