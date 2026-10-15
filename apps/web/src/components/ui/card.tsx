import * as React from 'react'
import { cn } from '@/lib/utils'

/** Flat panel: 1px border, 8px radius, no shadow. Header is a compact title bar. */
function Card({ className, ...props }: React.ComponentProps<'section'>) {
  return <section data-slot="card" className={cn('bg-card text-card-foreground rounded-lg border', className)} {...props} />
}

function CardHeader({ className, ...props }: React.ComponentProps<'header'>) {
  return <header data-slot="card-header" className={cn('flex min-h-10 items-center gap-2 border-b px-4 py-2', className)} {...props} />
}

function CardTitle({ className, ...props }: React.ComponentProps<'h2'>) {
  return <h2 data-slot="card-title" className={cn('text-sm font-medium', className)} {...props} />
}

function CardDescription({ className, ...props }: React.ComponentProps<'p'>) {
  return <p data-slot="card-description" className={cn('text-muted-foreground text-xs', className)} {...props} />
}

function CardContent({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="card-content" className={cn('p-4', className)} {...props} />
}

export { Card, CardContent, CardDescription, CardHeader, CardTitle }
