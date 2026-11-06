import type { SampleSeries } from './api'

export interface AlignedData {
  times: number[]
  values: (number | null)[][]
}

// alignSeries는 구간 전체에 시간 칸을 만들고 값이 없는 칸은 null로 둔다.
// 칸을 빼면 차트가 앞뒤 점을 이어 공백이 사라지기 때문이다.
export function alignSeries(series: SampleSeries[], fromMs: number, toMs: number, stepMs: number): AlignedData {
  const first = Math.floor(fromMs / stepMs) * stepMs
  const times: number[] = []
  for (let t = first; t <= toMs; t += stepMs) times.push(t)
  const values = series.map((item) => {
    const byTime = new Map(item.points.map((point) => [point.t, point.v]))
    return times.map((t) => byTime.get(t) ?? null)
  })
  return { times, values }
}

export function hasAnyValue(data: AlignedData): boolean {
  return data.values.some((values) => values.some((value) => value !== null))
}

const MIN_STEP_MS = 30_000
const MAX_POINTS = 300

// chartPoints는 샘플 주기의 흔들림이 공백으로 보이지 않도록 칸을 샘플 주기보다 넓게 잡는다.
export function chartPoints(fromMs: number, toMs: number): number {
  const byInterval = Math.floor((toMs - fromMs) / MIN_STEP_MS)
  return Math.max(2, Math.min(byInterval, MAX_POINTS))
}
