import { cn } from '@/lib/utils'

/** Alpine wordmark glyph: a single ridge line in the brand accent on ink. */
export function AlpineMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={cn('size-6', className)} aria-hidden>
      <rect width="24" height="24" rx="6" className="fill-primary" />
      <path d="M4.5 17 L9.5 9 L12.5 13 L14.5 10.5 L19.5 17" fill="none" strokeWidth="1.75" strokeLinejoin="round" strokeLinecap="round" className="stroke-primary-foreground" />
      <circle cx="17" cy="7" r="1.5" className="fill-brand" />
    </svg>
  )
}
