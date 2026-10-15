export function greet(name: string): string {
  const unused = 42;
  if (name == "") {
    return "hello";
  }
  return "hello " + name;
}
