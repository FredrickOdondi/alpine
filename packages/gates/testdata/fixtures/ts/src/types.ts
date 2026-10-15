interface Todo {
  title: string;
  done: boolean;
}

export function makeTodo(): Todo {
  return { done: false };
}

export const count: number = "three";
