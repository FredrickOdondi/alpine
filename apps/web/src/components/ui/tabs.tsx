import { Tabs as TabsPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

function Tabs({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return <TabsPrimitive.Root data-slot="tabs" className={cn('flex flex-col gap-3', className)} {...props} />
}

/** Segmented control look: 1px border, inset active pill. */
function TabsList({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn('bg-muted inline-flex h-8 w-fit items-center rounded-md border p-0.5', className)}
      {...props}
    />
  )
}

function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        'text-muted-foreground hover:text-foreground data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:border-border inline-flex h-full items-center gap-1.5 rounded-sm border border-transparent px-2.5 text-xs font-medium whitespace-nowrap transition-colors duration-150 disabled:opacity-40',
        className,
      )}
      {...props}
    />
  )
}

function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content data-slot="tabs-content" className={cn('flex-1', className)} {...props} />
}

export { Tabs, TabsContent, TabsList, TabsTrigger }
