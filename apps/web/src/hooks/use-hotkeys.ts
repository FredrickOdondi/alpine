import { useEffect, useRef } from 'react'

/** Return `false` to leave the key unhandled (no preventDefault). */
type Handler = (e: KeyboardEvent) => void | boolean

/** True when the key event originates from a text input, so single-key shortcuts stay out of the way. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)
}

/**
 * Bind keyboard shortcuts. Keys: 'mod+k', 'j', 'k', 'g b' (sequence), 'escape', '?'.
 * Single-key shortcuts are ignored while typing.
 */
export function useHotkeys(bindings: Record<string, Handler>, enabled = true) {
  const ref = useRef(bindings)
  useEffect(() => {
    ref.current = bindings
  })

  useEffect(() => {
    if (!enabled) return
    let pending: string | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    const onKey = (e: KeyboardEvent) => {
      const key = e.key.toLowerCase()
      const mod = e.metaKey || e.ctrlKey
      const combo = mod ? `mod+${key}` : key
      const map = ref.current
      if (mod) {
        if (map[combo]) {
          e.preventDefault()
          map[combo](e)
        }
        return
      }
      if (e.altKey || isTypingTarget(e.target)) return
      if (pending && map[`${pending} ${key}`]) {
        e.preventDefault()
        map[`${pending} ${key}`](e)
        pending = null
        return
      }
      if (Object.keys(map).some((k) => k.startsWith(`${key} `))) {
        pending = key
        clearTimeout(timer)
        timer = setTimeout(() => (pending = null), 800)
        return
      }
      if (map[combo] && map[combo](e) !== false) e.preventDefault()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      clearTimeout(timer)
    }
  }, [enabled])
}

/**
 * j/k list navigation over elements marked `data-nav-item` inside a container.
 * Enter follows the focused link (native behaviour).
 */
export function useListNavigation(containerSelector = '[data-nav-list]') {
  const move = (delta: number) => {
    const items = Array.from(document.querySelectorAll<HTMLElement>(`${containerSelector} [data-nav-item]`))
    if (items.length === 0) return
    const idx = items.findIndex((el) => el === document.activeElement || el.contains(document.activeElement))
    const next = items[Math.max(0, Math.min(items.length - 1, idx === -1 ? 0 : idx + delta))]
    next.focus()
    next.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }
  // Arrow keys only move while focus is already inside the list, so they still scroll the page
  // (e.g. an expanded inbox diff) everywhere else.
  const inList = () => !!document.activeElement?.closest(containerSelector)
  useHotkeys({
    j: () => move(1),
    k: () => move(-1),
    arrowdown: () => (inList() ? move(1) : false),
    arrowup: () => (inList() ? move(-1) : false),
  })
}
