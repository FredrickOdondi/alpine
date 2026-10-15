export function classify(n: number, strict: boolean): string {
  if (n < 0) return "negative";
  if (n === 0) return "zero";
  if (strict && n > 100) return "huge";
  if (n % 2 === 0 && n % 3 === 0) return "six-ish";
  if (n % 5 === 0 || n % 7 === 0) return "five-or-seven";
  return n > 10 ? "big" : "small";
}

export const scorer = (a: number, b: number): number => {
  if (a > b) return 1;
  if (a < b) return -1;
  if (a === 0 && b === 0) return 0;
  if (a > 100 || b > 100) return 2;
  if (a < -100) return -2;
  return a === b ? 0 : 3;
};
