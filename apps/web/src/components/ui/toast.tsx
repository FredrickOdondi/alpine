import { X } from 'lucide-react'
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface Toast {
  id: number
  message: ReactNode
  action?: { label: string; onClick: () => void }
  duration: number
}

const ToastContext = createContext<(t: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => void>(() => {})

export const useToast = () => useContext(ToastContext)

/** Minimal toaster: bottom-right stack, polite live region, auto-dismiss, optional action (e.g. Undo). */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const nextId = useRef(1)
  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), [])
  const push = useCallback((t: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => {
    const id = nextId.current++
    setToasts((ts) => [...ts.slice(-2), { duration: 6000, ...t, id }])
  }, [])

  return (
    <ToastContext value={push}>
      {children}
      <div role="status" aria-live="polite" className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-80 flex-col gap-2">
        {toasts.map((t) => (
          <ToastItem key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </ToastContext>
  )
}

function ToastItem({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onDismiss, toast.duration)
    return () => clearTimeout(timer)
  }, [toast.duration, onDismiss])
  return (
    <div
      className={cn(
        'bg-popover text-popover-foreground animate-in fade-in-0 slide-in-from-bottom-2 pointer-events-auto flex items-center gap-3 rounded-lg border py-2 pr-2 pl-3 text-sm shadow-[0_8px_24px_-8px_rgb(0_0_0/0.3)] duration-200 dark:border-border-strong',
      )}
    >
      <div className="min-w-0 flex-1">{toast.message}</div>
      {toast.action && (
        <button
          type="button"
          onClick={() => {
            toast.action!.onClick()
            onDismiss()
          }}
          className="text-brand hover:bg-accent h-7 rounded-md px-2 text-xs font-medium"
        >
          {toast.action.label}
        </button>
      )}
      <button type="button" onClick={onDismiss} aria-label="Dismiss" className="text-muted-foreground hover:bg-accent hover:text-foreground flex size-7 items-center justify-center rounded-md">
        <X className="size-4" />
      </button>
    </div>
  )
}
