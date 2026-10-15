import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

export type ThemePreference = 'light' | 'dark' | 'system'
const KEY = 'alpine.theme'

interface ThemeState {
  preference: ThemePreference
  resolved: 'light' | 'dark'
  setPreference: (p: ThemePreference) => void
  toggle: () => void
}

const ThemeContext = createContext<ThemeState | null>(null)

function systemTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function readPreference(): ThemePreference {
  const fromUrl = new URLSearchParams(window.location.search).get('theme')
  if (fromUrl === 'light' || fromUrl === 'dark') return fromUrl
  const stored = localStorage.getItem(KEY)
  return stored === 'light' || stored === 'dark' ? stored : 'system'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPref] = useState<ThemePreference>(readPreference)
  const [system, setSystem] = useState<'light' | 'dark'>(systemTheme)
  const resolved = preference === 'system' ? system : preference

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => setSystem(mq.matches ? 'dark' : 'light')
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  useEffect(() => {
    document.documentElement.classList.toggle('dark', resolved === 'dark')
  }, [resolved])

  const setPreference = useCallback((p: ThemePreference) => {
    setPref(p)
    if (p === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, p)
  }, [])

  const toggle = useCallback(() => setPreference(resolved === 'dark' ? 'light' : 'dark'), [resolved, setPreference])

  return <ThemeContext value={{ preference, resolved, setPreference, toggle }}>{children}</ThemeContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeState {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used inside ThemeProvider')
  return ctx
}
