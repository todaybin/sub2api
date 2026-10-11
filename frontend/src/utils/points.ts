export function isValidGroupPointsRate(enabled: boolean, rate: number): boolean {
  return Number.isFinite(rate) && rate >= 0 && rate <= 999999999999 &&
    (rate === 0 ? !enabled : rate >= 0.00000001)
}
