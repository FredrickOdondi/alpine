import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { metricsQuery, reposQuery } from '@/api/queries'
import type { Metrics } from '@/api/types'
import { ErrorState, PageHeader } from '@/components/app/page-header'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Segmented, SegmentedItem } from '@/components/ui/segmented'
import { formatPct, formatUsd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { RepoFilter } from './board'

interface Props {
  window: Metrics['window']
  repoId?: string
  onChange: (next: { window?: Metrics['window']; repo?: string }) => void
}

const axis = { stroke: 'var(--muted-foreground)', fontSize: 11, tickLine: false, axisLine: false } as const
const tooltip = {
  contentStyle: { background: 'var(--popover)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--foreground)', boxShadow: 'none' },
  labelStyle: { color: 'var(--muted-foreground)' },
  cursor: { fill: 'var(--accent)', stroke: 'var(--border-strong)' },
} as const
const shortDate = (d: string) => new Date(d).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })

export function MetricsPage({ window, repoId, onChange }: Props) {
  const m = useQuery(metricsQuery(window, repoId))
  const repos = useQuery(reposQuery)
  const data = m.data

  return (
    <div className="flex min-h-screen flex-col">
      <PageHeader
        title="Metrics"
        meta="Improve the factory, not individual PRs"
        actions={
          <>
            <RepoFilter repos={repos.data?.items ?? []} value={repoId} onChange={(repo) => onChange({ repo })} />
            <Segmented value={window} onValueChange={(v: Metrics['window']) => onChange({ window: v })} aria-label="Time window">
              <SegmentedItem value="7d">7d</SegmentedItem>
              <SegmentedItem value="30d">30d</SegmentedItem>
              <SegmentedItem value="90d">90d</SegmentedItem>
            </Segmented>
          </>
        }
      />
      {m.error ? (
        <ErrorState error={m.error} onRetry={() => void m.refetch()} />
      ) : (
        <div className="space-y-4 p-4">
          <dl className="bg-card grid grid-cols-2 divide-y rounded-lg border sm:grid-cols-3 sm:divide-y-0 xl:grid-cols-6 xl:divide-x">
            <Kpi label="Merged tickets" value={data && String(data.totals.ticketsMerged)} />
            <Kpi label="Turns p50 / p90" value={data && `${data.totals.turnsP50} / ${data.totals.turnsP90}`} />
            <Kpi label="Cost per merged ticket" value={data && formatUsd(data.totals.costPerTicketUsd)} sub={data && `${formatUsd(data.totals.costTotalUsd)} total`} />
            <Kpi label="First-run pass rate" value={data && formatPct(data.totals.gatePassRate)} sub="all gates, window average" />
            <Kpi label="Escalation rate" value={data && formatPct(data.totals.escalationRate, 1)} />
            <Kpi label="Lead time p50" value={data && `${data.totals.leadTimeHoursP50.toFixed(1)}h`} sub="intake → merge" />
          </dl>

          <div className="grid gap-4 xl:grid-cols-2">
            <ChartCard
              title="Turns per ticket"
              description="Implementer turns, 100-turn cap"
              legend={
                <>
                  <LegendKey color="var(--chart-1)" label="p50" />
                  <LegendKey color="var(--chart-2)" label="p90" dashed />
                  <LegendKey color="var(--fail)" label="Cap" dotted />
                </>
              }
            >
              {data && (
                <LineChart data={data.series} margin={{ top: 8, right: 8, bottom: 0, left: -16 }}>
                  <CartesianGrid stroke="var(--border)" vertical={false} />
                  <XAxis dataKey="date" tickFormatter={shortDate} minTickGap={32} {...axis} />
                  <YAxis domain={[0, 110]} ticks={[0, 25, 50, 75, 100]} {...axis} />
                  <ReferenceLine y={100} stroke="var(--fail)" strokeDasharray="2 3" ifOverflow="extendDomain" />
                  <Tooltip {...tooltip} labelFormatter={(l) => shortDate(String(l))} />
                  <Line type="monotone" dataKey="turnsP90" name="p90" stroke="var(--chart-2)" strokeWidth={1.5} strokeDasharray="4 3" dot={false} isAnimationActive={false} />
                  <Line type="monotone" dataKey="turnsP50" name="p50" stroke="var(--chart-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
                </LineChart>
              )}
            </ChartCard>
            <ChartCard title="Model + sandbox cost" description="Daily spend across merged and in-flight tickets.">
              {data && (
                <BarChart data={data.series} margin={{ top: 8, right: 8, bottom: 0, left: -8 }}>
                  <CartesianGrid stroke="var(--border)" vertical={false} />
                  <XAxis dataKey="date" tickFormatter={shortDate} minTickGap={32} {...axis} />
                  <YAxis tickFormatter={(v: number) => `$${v}`} {...axis} />
                  <Tooltip {...tooltip} labelFormatter={(l) => shortDate(String(l))} formatter={(v) => formatUsd(Number(v))} />
                  <Bar dataKey="costUsd" name="Cost" fill="var(--chart-1)" radius={[2, 2, 0, 0]} maxBarSize={14} isAnimationActive={false} />
                </BarChart>
              )}
            </ChartCard>
          </div>

          <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
            <ChartCard title="Gate pass rate" description="Share of gate runs that pass on the first attempt.">
              {data && (
                <LineChart data={data.series} margin={{ top: 8, right: 8, bottom: 0, left: -12 }}>
                  <CartesianGrid stroke="var(--border)" vertical={false} />
                  <XAxis dataKey="date" tickFormatter={shortDate} minTickGap={32} {...axis} />
                  <YAxis domain={[0.4, 1]} ticks={[0.4, 0.6, 0.8, 1]} tickFormatter={(v: number) => formatPct(v)} {...axis} />
                  <Tooltip {...tooltip} labelFormatter={(l) => shortDate(String(l))} formatter={(v) => formatPct(Number(v))} />
                  <Line type="monotone" dataKey="gatePassRate" name="Pass rate" stroke="var(--chart-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
                </LineChart>
              )}
            </ChartCard>
            <Card>
              <CardHeader><CardTitle>By gate</CardTitle><CardDescription className="ml-auto">pass rate · runs</CardDescription></CardHeader>
              {data ? (
                <Table>
                  <TableHeader>
                    <TableRow><TableHead>Gate</TableHead><TableHead className="w-1/2">Pass rate</TableHead><TableHead className="text-right">Runs</TableHead></TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.gatePassRateByGate.map((g) => (
                      <TableRow key={g.gateId}>
                        <TableCell className="font-mono text-xs">{g.gateId}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <div className="bg-muted h-1.5 flex-1 overflow-hidden rounded-full" aria-hidden>
                              <div className={cn('h-full', g.passRate < 0.7 ? 'bg-warn' : 'bg-foreground/70')} style={{ width: formatPct(g.passRate) }} />
                            </div>
                            <span className="w-10 text-right text-xs">{formatPct(g.passRate)}</span>
                          </div>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-right text-xs">{g.runs}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <div className="space-y-2 p-4">{Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="h-5" />)}</div>
              )}
            </Card>
          </div>

          <Card>
            <CardHeader><CardTitle>Top gate failure reasons</CardTitle><CardDescription className="ml-auto">Fix these in rules, seeds or gates</CardDescription></CardHeader>
            {data ? (
              <Table>
                <TableHeader><TableRow><TableHead>Gate</TableHead><TableHead>Rule</TableHead><TableHead className="text-right">Failures</TableHead></TableRow></TableHeader>
                <TableBody>
                  {data.topFailureReasons.map((r) => (
                    <TableRow key={r.rule}>
                      <TableCell className="font-mono text-xs">{r.gateId}</TableCell>
                      <TableCell>{r.rule}</TableCell>
                      <TableCell className="text-right">{r.count}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              <div className="space-y-2 p-4">{Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-5" />)}</div>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}

function Kpi({ label, value, sub }: { label: string; value?: string; sub?: string }) {
  return (
    <div className="space-y-1 px-4 py-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="tnum text-xl font-semibold tracking-tight">{value ?? <Skeleton className="h-7 w-20" />}</dd>
      <dd className="text-muted-foreground h-4 text-xs">{sub}</dd>
    </div>
  )
}

function LegendKey({ color, label, dashed, dotted }: { color: string; label: string; dashed?: boolean; dotted?: boolean }) {
  return (
    <span className="text-muted-foreground flex items-center gap-1.5 text-xs">
      <svg width="16" height="8" aria-hidden>
        <line x1="0" y1="4" x2="16" y2="4" stroke={color} strokeWidth="2" strokeDasharray={dotted ? '2 3' : dashed ? '4 3' : undefined} />
      </svg>
      {label}
    </span>
  )
}

function ChartCard({
  title,
  description,
  legend,
  children,
}: {
  title: string
  description: string
  legend?: React.ReactNode
  children: React.ReactElement | undefined | false
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription className="truncate">{description}</CardDescription>
        {legend && <div className="ml-auto flex items-center gap-3">{legend}</div>}
      </CardHeader>
      <CardContent className="h-56 pt-3">
        {children ? (
          <ResponsiveContainer width="100%" height="100%">{children}</ResponsiveContainer>
        ) : (
          <Skeleton className="h-full w-full" />
        )}
      </CardContent>
    </Card>
  )
}
