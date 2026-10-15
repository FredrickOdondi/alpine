import { CircleAlert, RotateCw } from 'lucide-react'
import { useEffect, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/**
 * 48px top bar. `title` is the page's h1 unless `heading={false}` (e.g. the ticket page, whose
 * h1 is the ticket title and whose bar holds a breadcrumb nav).
 */
export function PageHeader({ title, meta, actions, heading = true }: { title: ReactNode; meta?: ReactNode; actions?: ReactNode; heading?: boolean }) {
  const Title = heading ? 'h1' : 'div'
  return (
    <header className="bg-background/95 sticky top-0 z-10 flex h-12 shrink-0 items-center gap-3 border-b px-4">
      <Title className={cn('truncate', heading ? 'text-base font-semibold' : 'text-sm')}>{title}</Title>
      {meta && <div className="text-muted-foreground hidden truncate text-xs md:block">{meta}</div>}
      {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
    </header>
  )
}

/** Centers a state block at ~40% of the available height instead of floating at the top. */
function StateFrame({ children }: { children: ReactNode }) {
  return <div className="flex flex-1 items-start justify-center px-4 pt-[16vh] pb-16">{children}</div>
}

/**
 * Full-view error: friendly copy, raw message behind a disclosure, Retry, and optional
 * auto-retry so a transient upstream failure heals itself.
 */
export function ErrorState({
  error,
  onRetry,
  title = 'Couldn’t load this view',
  body = 'Alpine’s API didn’t respond.',
  autoRetryMs = 10_000,
}: {
  error: Error
  onRetry?: () => void
  title?: string
  body?: string
  autoRetryMs?: number | null
}) {
  useEffect(() => {
    if (!onRetry || !autoRetryMs) return
    const timer = setInterval(onRetry, autoRetryMs)
    return () => clearInterval(timer)
  }, [onRetry, autoRetryMs])
  return (
    <StateFrame>
      <div role="alert" className="flex max-w-md flex-col items-center gap-4 text-center">
        <div className="text-fail flex size-9 items-center justify-center rounded-md border">
          <CircleAlert className="size-4" aria-hidden />
        </div>
        <div className="space-y-1">
          <p className="text-base font-semibold">{title}</p>
          <p className="text-muted-foreground">
            {body}
            {onRetry && autoRetryMs ? ` Retrying every ${Math.round(autoRetryMs / 1000)}s.` : ''}
          </p>
        </div>
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RotateCw /> Retry now
          </Button>
        )}
        <details className="text-muted-foreground text-xs">
          <summary className="hover:text-foreground cursor-pointer">Details</summary>
          <p className="mt-2 font-mono break-all">{error.message}</p>
        </details>
      </div>
    </StateFrame>
  )
}

/** One-line error for a section inside an otherwise working view. */
export function InlineError({ label, onRetry }: { label: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="flex h-8 items-center gap-2 rounded-md border px-3 text-xs">
      <CircleAlert className="text-fail size-4 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {onRetry && (
        <button type="button" onClick={onRetry} className="text-brand hover:underline">
          Retry
        </button>
      )}
    </div>
  )
}

export function EmptyState({ icon, title, body, action }: { icon: ReactNode; title: string; body: ReactNode; action?: ReactNode }) {
  return (
    <StateFrame>
      <div className="flex max-w-sm flex-col items-center gap-4 text-center">
        <div className="text-muted-foreground flex size-9 items-center justify-center rounded-md border [&_svg]:size-4">{icon}</div>
        <div className="space-y-1">
          <p className="text-base font-semibold">{title}</p>
          <p className="text-muted-foreground">{body}</p>
        </div>
        {action && <div className="flex flex-wrap justify-center gap-2">{action}</div>}
      </div>
    </StateFrame>
  )
}
