export enum Color {
  Red = "red",
  Purple = "purple",
}

export type DateFmt = "iso" | "short";

export function formatDate(d: Date, fmt: DateFmt): string {
  return fmt === "iso" ? d.toISOString() : d.toDateString();
}
