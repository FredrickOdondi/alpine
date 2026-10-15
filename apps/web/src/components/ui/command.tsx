import { Command as CommandPrimitive } from 'cmdk'
import { Search } from 'lucide-react'
import * as React from 'react'
import { cn } from '@/lib/utils'

function Command({ className, ...props }: React.ComponentProps<typeof CommandPrimitive>) {
  return <CommandPrimitive className={cn('flex h-full w-full flex-col', className)} {...props} />
}

function CommandInput({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Input>) {
  return (
    <div className="flex h-11 items-center gap-2 border-b px-3">
      <Search className="text-muted-foreground size-4" aria-hidden />
      <CommandPrimitive.Input
        className={cn('placeholder:text-muted-foreground h-full flex-1 bg-transparent text-base outline-none focus-visible:outline-none', className)}
        {...props}
      />
    </div>
  )
}

function CommandList({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.List>) {
  return <CommandPrimitive.List className={cn('max-h-80 overflow-y-auto p-1.5', className)} {...props} />
}

function CommandEmpty(props: React.ComponentProps<typeof CommandPrimitive.Empty>) {
  return <CommandPrimitive.Empty className="text-muted-foreground py-8 text-center text-sm" {...props} />
}

function CommandGroup({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      className={cn('[&_[cmdk-group-heading]]:text-muted-foreground py-1 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-2xs [&_[cmdk-group-heading]]:font-medium', className)}
      {...props}
    />
  )
}

function CommandItem({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      className={cn(
        'data-[selected=true]:bg-accent flex h-8 cursor-default items-center gap-2 rounded-md px-2 text-sm select-none data-[disabled=true]:opacity-50 [&_svg]:size-4 [&_svg]:text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

export { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList }
