import { Popover as PopoverPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

const Popover = PopoverPrimitive.Root
const PopoverTrigger = PopoverPrimitive.Trigger
const PopoverClose = PopoverPrimitive.Close
const PopoverAnchor = PopoverPrimitive.Anchor

function PopoverContent({ className, align = 'end', sideOffset = 6, ...props }: React.ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        sideOffset={sideOffset}
        className={cn(
          'bg-popover text-popover-foreground data-[state=open]:animate-in data-[state=open]:fade-in-0 z-50 w-80 rounded-lg border p-3 shadow-[0_8px_24px_-8px_rgb(0_0_0/0.25)] duration-150 dark:border-border-strong',
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  )
}

export { Popover, PopoverAnchor, PopoverClose, PopoverContent, PopoverTrigger }
