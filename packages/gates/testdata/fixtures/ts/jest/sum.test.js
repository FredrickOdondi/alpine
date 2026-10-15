const sum = (a, b) => a + b;

test("sum adds positives", () => {
  expect(sum(1, 2)).toBe(3);
});

test("sum adds negatives", () => {
  expect(sum(-1, -1)).toBe(-3);
});

test("sum adds zero", () => {
  expect(sum(0, 0)).toBe(0);
});
