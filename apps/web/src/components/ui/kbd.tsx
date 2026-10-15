import * as React from 'react'
import { cn } from '@/lib/utils'

function Kbd({ className, ...props }: React.ComponentProps<'kbd'>) {
  return <kbd data-slot="kbd" className={cn('kbd', className)} {...props} />
}

export { Kbd }
