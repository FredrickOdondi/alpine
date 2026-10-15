import { ChevronDown } from 'lucide-react'
import * as React from 'react'
import { cn } from '@/lib/utils'

/** Native select styled to Alpine tokens (accessible, tiny). */
function NativeSelect({ className, children, ...props }: React.ComponentProps<'select'>) {
  return (
    <div className={cn('relative inline-flex', className)}>
      <select
        data-slot="native-select"
        className="border-input bg-card hover:bg-accent h-8 w-full appearance-none rounded-md border pr-7 pl-2.5 text-sm transition-colors duration-150"
        {...props}
      >
        {children}
      </select>
      <ChevronDown className="text-muted-foreground pointer-events-none absolute top-1/2 right-2 size-3.5 -translate-y-1/2" />
    </div>
  )
}

export { NativeSelect }
