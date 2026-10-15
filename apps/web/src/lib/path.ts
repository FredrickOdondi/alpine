/** Middle-truncate a file path so the useful end survives: "src/…/Modal/useFocusTrap.ts". */
export function middleTruncatePath(path: string, max = 44): string {
  if (path.length <= max) return path
  const parts = path.split('/')
  if (parts.length <= 3) return `…${path.slice(-(max - 1))}`
  const tail = parts.slice(-2).join('/')
  const head = parts[0]
  const short = `${head}/…/${tail}`
  return short.length <= max ? short : `…/${tail}`
}
