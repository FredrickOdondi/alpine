import { Tooltip as TooltipPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

/** Small visible tooltip (shows on hover and keyboard focus). */
export function Tooltip({ content, children, side = 'top' }: { content: React.ReactNode; children: React.ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  return (
    <TooltipPrimitive.Provider delayDuration={250}>
      <TooltipPrimitive.Root>
        <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            sideOffset={6}
            className={cn('bg-primary text-primary-foreground animate-in fade-in-0 z-50 rounded-md px-2 py-1 text-xs duration-150')}
          >
            {content}
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  )
}
