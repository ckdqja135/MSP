import { describe, expect, it } from 'vitest'
import { alignSeries, chartPoints, hasAnyValue } from './chartData'

describe('alignSeries', () => {
  it('값이 없는 칸은 0이 아니라 null이다', () => {
    const data = alignSeries([{ name: 'm', points: [{ t: 0, v: 5 }, { t: 3000, v: 0 }] }], 0, 3000, 1000)
    expect(data.times).toEqual([0, 1000, 2000, 3000])
    expect(data.values[0]).toEqual([5, null, null, 0])
  })

  it('여러 계열을 같은 시간축에 맞춘다', () => {
    const data = alignSeries(
      [
        { name: 'a', points: [{ t: 1000, v: 1 }] },
        { name: 'b', points: [{ t: 2000, v: 2 }] },
      ],
      1000,
      2000,
      1000,
    )
    expect(data.values).toEqual([[1, null], [null, 2]])
  })
})

describe('hasAnyValue', () => {
  it('값이 하나도 없는 경우를 구분한다', () => {
    expect(hasAnyValue({ times: [0], values: [[null]] })).toBe(false)
    expect(hasAnyValue({ times: [0], values: [[0]] })).toBe(true)
  })
})

describe('chartPoints', () => {
  it('칸 폭이 30초보다 좁아지지 않는다', () => {
    expect(chartPoints(0, 15 * 60_000)).toBe(30)
    expect(chartPoints(0, 24 * 60 * 60_000)).toBe(300)
  })
})
