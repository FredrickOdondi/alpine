import { cva, type VariantProps } from 'class-variance-authority'
import * as React from 'react'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex h-5 shrink-0 items-center gap-1 rounded-sm px-1.5 text-2xs font-medium whitespace-nowrap [&>svg]:size-3',
  {
    variants: {
      variant: {
        neutral: 'bg-secondary text-secondary-foreground',
        outline: 'border border-border-strong text-muted-foreground',
        brand: 'bg-brand-subtle text-brand',
        ok: 'bg-ok-subtle text-ok',
        warn: 'bg-warn-subtle text-warn',
        fail: 'bg-fail-subtle text-fail',
        info: 'bg-info-subtle text-info',
      },
    },
    defaultVariants: { variant: 'neutral' },
  },
)

function Badge({ className, variant, ...props }: React.ComponentProps<'span'> & VariantProps<typeof badgeVariants>) {
  return <span data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />
}

export { Badge, badgeVariants }
