import { ToggleGroup as ToggleGroupPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

/**
 * Segmented control built on Radix ToggleGroup (single). Use this, not Tabs, when the control
 * switches a view or filter without owning tab panels: Tabs without panels leave aria-controls
 * dangling (axe: aria-valid-attr-value).
 */
function Segmented<T extends string>({
  value,
  onValueChange,
  className,
  children,
  ...props
}: Omit<React.ComponentProps<typeof ToggleGroupPrimitive.Root>, 'type' | 'value' | 'defaultValue' | 'onValueChange'> & {
  value: T
  onValueChange: (value: T) => void
}) {
  return (
    <ToggleGroupPrimitive.Root
      type="single"
      value={value}
      // A segmented control always has one value: ignore attempts to deselect.
      onValueChange={(v) => v && onValueChange(v as T)}
      className={cn('border-input bg-muted inline-flex h-8 w-fit items-center rounded-md border p-0.5', className)}
      {...props}
    >
      {children}
    </ToggleGroupPrimitive.Root>
  )
}

function SegmentedItem({ className, ...props }: React.ComponentProps<typeof ToggleGroupPrimitive.Item>) {
  return (
    <ToggleGroupPrimitive.Item
      className={cn(
        'text-muted-foreground hover:text-foreground data-[state=on]:bg-card data-[state=on]:text-foreground data-[state=on]:border-input inline-flex h-full items-center gap-1.5 rounded-sm border border-transparent px-2 text-xs font-medium whitespace-nowrap transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}

export { Segmented, SegmentedItem }
