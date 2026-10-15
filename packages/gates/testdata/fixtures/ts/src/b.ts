export function sumB(values: number[]): number {
  let total = 0;
  for (const v of values) {
    if (Number.isFinite(v)) {
      total += v;
    } else {
      throw new Error("not finite: " + String(v));
    }
  }
  const avg = values.length ? total / values.length : 0;
  const label = avg > 10 ? "high" : avg > 5 ? "mid" : "low";
  console.log("sum", total, "avg", avg, "label", label);
  return total;
}
