import * as React from 'react'
import { cn } from '@/lib/utils'

function Skeleton({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="skeleton" aria-hidden className={cn('bg-secondary dark:bg-border animate-pulse rounded-sm', className)} {...props} />
}

export { Skeleton }
