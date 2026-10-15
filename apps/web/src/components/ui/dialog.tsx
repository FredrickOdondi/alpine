import { Dialog as DialogPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

const Dialog = DialogPrimitive.Root
const DialogTitle = DialogPrimitive.Title
const DialogDescription = DialogPrimitive.Description
const DialogClose = DialogPrimitive.Close

function DialogContent({ className, children, ...props }: React.ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 fixed inset-0 z-50 bg-black/40 duration-150 dark:bg-black/65" />
      <DialogPrimitive.Content
        className={cn(
          'bg-popover text-popover-foreground data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-[0.98] fixed top-[18%] left-1/2 z-50 w-full max-w-xl -translate-x-1/2 overflow-hidden rounded-lg border dark:border-border-strong shadow-[0_16px_48px_-12px_rgb(0_0_0/0.35)] duration-150',
          className,
        )}
        {...props}
      >
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  )
}

export { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle }
