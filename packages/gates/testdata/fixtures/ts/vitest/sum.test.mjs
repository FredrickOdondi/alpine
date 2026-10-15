import { expect, test } from "vitest";

const sum = (a, b) => a + b;

test("sum adds positives", () => {
  expect(sum(1, 2)).toBe(3);
});

test("sum adds negatives", () => {
  expect(sum(-1, -1)).toBe(-2);
});
