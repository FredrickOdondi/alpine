import dayjs from "dayjs";
import { greet } from "./app";
import { classify } from "./big";
import { sumA } from "./a";
import { sumB } from "./b";
import { Color } from "./dates";

export function main(): string {
  return [greet("x"), classify(3, true), sumA([1]), sumB([2]), Color.Red, dayjs().year()].join(" ");
}
