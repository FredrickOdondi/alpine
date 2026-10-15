import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { ChartColumn, GitPullRequest, Keyboard, LayoutGrid, Moon, Settings2, Sun, Ticket as TicketIcon } from 'lucide-react'
import { createContext, useContext, useState, type ReactNode } from 'react'
import { reposQuery, ticketsQuery } from '@/api/queries'
import { STAGE_LABEL } from '@/api/types'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Kbd } from '@/components/ui/kbd'
import { useTheme } from '@/design/theme'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { StageDot } from './badges'

const PaletteContext = createContext<{ open: () => void }>({ open: () => {} })
// eslint-disable-next-line react-refresh/only-export-components
export const useCommandPalette = () => useContext(PaletteContext)

export function CommandPaletteProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const [help, setHelp] = useState(false)
  const navigate = useNavigate()
  const theme = useTheme()
  const tickets = useQuery({ ...ticketsQuery(), enabled: open })
  const repos = useQuery({ ...reposQuery, enabled: open })

  useHotkeys({
    'mod+k': () => setOpen((o) => !o),
    'g b': () => void navigate({ to: '/board' }),
    'g i': () => void navigate({ to: '/inbox' }),
    'g m': () => void navigate({ to: '/metrics' }),
    'g c': () => void navigate({ to: '/repos' }),
    '?': () => setHelp((h) => !h),
  })

  const run = (fn: () => void) => {
    setOpen(false)
    fn()
  }

  return (
    <PaletteContext value={{ open: () => setOpen(true) }}>
      {children}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent aria-describedby="cmdk-desc">
          <DialogTitle className="sr-only">Command palette</DialogTitle>
          <DialogDescription id="cmdk-desc" className="sr-only">Search tickets, jump to a screen, or run an action.</DialogDescription>
          <Command loop>
            <CommandInput placeholder="Search tickets or jump to…" aria-label="Command" />
            <CommandList>
              <CommandEmpty>No matches.</CommandEmpty>
              <CommandGroup heading="Go to">
                <CommandItem onSelect={() => run(() => navigate({ to: '/board' }))}><LayoutGrid />Pipeline<Shortcut keys={['G', 'B']} /></CommandItem>
                <CommandItem onSelect={() => run(() => navigate({ to: '/inbox' }))}><GitPullRequest />PR inbox<Shortcut keys={['G', 'I']} /></CommandItem>
                <CommandItem onSelect={() => run(() => navigate({ to: '/metrics' }))}><ChartColumn />Metrics<Shortcut keys={['G', 'M']} /></CommandItem>
                {repos.data?.items.map((r) => (
                  <CommandItem key={r.id} value={`config ${r.fullName}`} onSelect={() => run(() => navigate({ to: '/repos/$repoId/config', params: { repoId: r.id } }))}>
                    <Settings2 />Factory config · <span className="font-mono">{r.fullName}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
              <CommandGroup heading="Actions">
                <CommandItem onSelect={() => run(() => setHelp(true))}>
                  <Keyboard />
                  Keyboard shortcuts
                  <Shortcut keys={['?']} />
                </CommandItem>
                <CommandItem onSelect={() => run(theme.toggle)}>
                  {theme.resolved === 'dark' ? <Sun /> : <Moon />}
                  Switch to {theme.resolved === 'dark' ? 'light' : 'dark'} theme
                </CommandItem>
              </CommandGroup>
              {tickets.data && (
                <CommandGroup heading="Tickets">
                  {tickets.data.items.map((t) => (
                    <CommandItem key={t.id} value={`${t.key} ${t.title}`} onSelect={() => run(() => navigate({ to: '/tickets/$ticketId', params: { ticketId: t.id } }))}>
                      <TicketIcon />
                      <span className="text-muted-foreground w-16 shrink-0 font-mono text-xs">{t.key}</span>
                      <span className="truncate">{t.title}</span>
                      <span className="text-muted-foreground ml-auto flex shrink-0 items-center gap-1.5 text-xs">
                        <StageDot stage={t.stage} />
                        {STAGE_LABEL[t.stage]}
                      </span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              )}
            </CommandList>
          </Command>
          <div className="text-muted-foreground flex items-center gap-3 border-t px-3 py-2 text-2xs">
            <span className="flex items-center gap-1"><Kbd>↑</Kbd><Kbd>↓</Kbd> navigate</span>
            <span className="flex items-center gap-1"><Kbd>↵</Kbd> open</span>
            <span className="flex items-center gap-1"><Kbd>esc</Kbd> close</span>
          </div>
        </DialogContent>
      </Dialog>
      <ShortcutsSheet open={help} onOpenChange={setHelp} />
    </PaletteContext>
  )
}

function Shortcut({ keys }: { keys: string[] }) {
  return (
    <span className="ml-auto flex gap-0.5" aria-hidden>
      {keys.map((k) => <Kbd key={k}>{k}</Kbd>)}
    </span>
  )
}

const SHORTCUTS: { group: string; items: [string[], string][] }[] = [
  { group: 'Global', items: [[['⌘', 'K'], 'Command palette'], [['?'], 'Keyboard shortcuts'], [['['], 'Collapse or expand the sidebar'], [['G', 'B'], 'Go to pipeline'], [['G', 'I'], 'Go to PR inbox'], [['G', 'M'], 'Go to metrics'], [['G', 'C'], 'Go to factory config']] },
  { group: 'Pipeline', items: [[['J', 'K'], 'Next / previous ticket in a column'], [['H', 'L'], 'Previous / next column'], [['↵'], 'Open ticket']] },
  { group: 'PR inbox', items: [[['J', 'K'], 'Next / previous pull request'], [['X'], 'Expand evidence'], [['A'], 'Approve merge (confirm scales with blast radius)'], [['R'], 'Request changes'], [['O'], 'Open ticket']] },
  { group: 'Ticket', items: [[['Esc'], 'Back'], [['←', '→'], 'Previous / next screenshot in the viewer']] },
]

function ShortcutsSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <div className="flex h-12 items-center border-b px-4">
          <DialogTitle className="text-sm font-semibold">Keyboard shortcuts</DialogTitle>
        </div>
        <DialogDescription className="sr-only">All keyboard shortcuts in Alpine.</DialogDescription>
        <div className="grid max-h-[60vh] gap-6 overflow-y-auto p-4 sm:grid-cols-2">
          {SHORTCUTS.map((g) => (
            <section key={g.group} className="space-y-2">
              <h3 className="text-xs font-semibold">{g.group}</h3>
              <dl className="space-y-2">
                {g.items.map(([keys, label]) => (
                  <div key={label} className="flex items-center justify-between gap-3 text-xs">
                    <dt className="text-muted-foreground">{label}</dt>
                    <dd className="flex shrink-0 gap-1">{keys.map((k) => <Kbd key={k}>{k}</Kbd>)}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
