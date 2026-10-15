import * as React from 'react'
import { cn } from '@/lib/utils'

/** Compact data table: 36px rows, hairline separators, tabular numerals. */
function Table({ className, ...props }: React.ComponentProps<'table'>) {
  return (
    <div className="relative w-full overflow-x-auto">
      <table data-slot="table" className={cn('tnum w-full caption-bottom text-sm', className)} {...props} />
    </div>
  )
}
function TableHeader({ className, ...props }: React.ComponentProps<'thead'>) {
  return <thead className={cn('[&_tr]:border-b', className)} {...props} />
}
function TableBody({ className, ...props }: React.ComponentProps<'tbody'>) {
  return <tbody className={cn('[&_tr:last-child]:border-0', className)} {...props} />
}
function TableRow({ className, ...props }: React.ComponentProps<'tr'>) {
  return <tr className={cn('border-b transition-colors duration-150', className)} {...props} />
}
function TableHead({ className, ...props }: React.ComponentProps<'th'>) {
  return <th className={cn('text-muted-foreground h-8 px-3 text-left align-middle text-xs font-medium whitespace-nowrap', className)} {...props} />
}
function TableCell({ className, ...props }: React.ComponentProps<'td'>) {
  return <td className={cn('h-9 px-3 align-middle', className)} {...props} />
}

export { Table, TableBody, TableCell, TableHead, TableHeader, TableRow }
