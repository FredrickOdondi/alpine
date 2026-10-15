import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { ArrowUpRight, CircleCheck, CircleX, FileCode, Lock } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { factoryConfigQuery, reposQuery } from '@/api/queries'
import { brLabel, type BlastRadius, type FactoryConfig } from '@/api/types'
import { BrBadge } from '@/components/app/badges'
import { EmptyState, ErrorState, PageHeader } from '@/components/app/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { NativeSelect } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Segmented, SegmentedItem } from '@/components/ui/segmented'
import { brTone } from '@/design/tokens'
import { timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'
import { ApiRequestError } from '@/api/client'

const BRS: BlastRadius[] = [4, 3, 2, 1]

export function RepoConfigPage({ repoId }: { repoId: string }) {
  const cfg = useQuery(factoryConfigQuery(repoId))
  const repos = useQuery(reposQuery)
  const navigate = useNavigate()
  const [view, setView] = useState<'overview' | 'yaml'>('overview')
  const repo = repos.data?.items.find((r) => r.id === repoId)
  const editUrl = repo && `https://github.com/${repo.fullName}/edit/${repo.defaultBranch}/.alpine/factory.yaml`

  return (
    <div className="flex min-h-screen flex-col">
      <PageHeader
        title="Factory config"
        meta={cfg.data && (
          <span className="flex items-center gap-1.5">
            <FileCode className="size-3.5" aria-hidden />
            <span className="font-mono">{cfg.data.path}</span> @ <span className="font-mono">{cfg.data.sha}</span> · updated {timeAgo(cfg.data.updatedAt)}
          </span>
        )}
        actions={
          <>
            <NativeSelect
              value={repoId}
              onChange={(e) => void navigate({ to: '/repos/$repoId/config', params: { repoId: e.target.value } })}
              aria-label="Repository"
              className="w-48"
            >
              {repos.data?.items.map((r) => <option key={r.id} value={r.id}>{r.fullName}</option>)}
            </NativeSelect>
            <Segmented value={view} onValueChange={(v: 'overview' | 'yaml') => setView(v)} aria-label="Config view">
              <SegmentedItem value="overview">Overview</SegmentedItem>
              <SegmentedItem value="yaml">factory.yaml</SegmentedItem>
            </Segmented>
            {editUrl && (
              <Button variant="outline" size="sm" asChild>
                <a href={editUrl} target="_blank" rel="noreferrer">Edit via PR <ArrowUpRight /></a>
              </Button>
            )}
          </>
        }
      />
      {cfg.error instanceof ApiRequestError && cfg.error.status === 404 ? (
        <EmptyState
          icon={<FileCode />}
          title="No factory config on the default branch"
          body="Add .alpine/factory.yaml to the repository. Alpine reads it from the default branch on every push."
          action={editUrl && <Button size="sm" asChild><a href={editUrl.replace('/edit/', '/new/')} target="_blank" rel="noreferrer">Create factory.yaml</a></Button>}
        />
      ) : cfg.error ? (
        <ErrorState error={cfg.error} onRetry={() => void cfg.refetch()} />
      ) : !cfg.data ? (
        <div className="grid gap-4 p-4 xl:grid-cols-2" aria-busy="true">{Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-48" />)}</div>
      ) : (
        <div className="space-y-4 p-4">
          <p className="text-muted-foreground flex items-center gap-2 text-xs">
            {cfg.data.validation.valid ? <CircleCheck className="text-ok size-3.5" aria-hidden /> : <CircleX className="text-fail size-3.5" aria-hidden />}
            {cfg.data.validation.valid ? 'Schema valid.' : cfg.data.validation.errors.join(' ')} The repository is the source of truth: changes are made by opening a PR, and gate config is always read from the base branch.
          </p>
          {view === 'yaml' ? <YamlView yaml={cfg.data.rawYaml} /> : <Overview cfg={cfg.data} />}
        </div>
      )}
    </div>
  )
}

function Overview({ cfg }: { cfg: FactoryConfig }) {
  const p = cfg.parsed
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Panel title="Limits" description="Escalate to a human when any cap is hit">
        <dl className="tnum grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
          <Stat label="Max turns" value={p.limits.max_turns} />
          <Stat label="Gate iterations" value={p.limits.max_gate_iterations} />
          <Stat label="Cost cap" value={`$${p.limits.max_cost_usd}`} />
          <Stat label="Parallel tickets" value={p.limits.max_parallel_tickets} />
        </dl>
      </Panel>

      <Panel title="Merge policy" description="By final blast radius">
        {(() => {
          const values = BRS.map((br) => p.merge_policy[brLabel(br)])
          const uniform = values.every((v) => v === values[0])
          return (
            <div className="space-y-3">
              {uniform && (
                <p className="text-sm">
                  {values[0] === 'auto' ? 'Every blast radius auto-merges once gates and review pass.' : 'Every blast radius requires a human merge.'}
                </p>
              )}
              <dl className="grid grid-cols-4 gap-4">
                {BRS.slice().reverse().map((br) => (
                  <div key={br} className="space-y-1">
                    <dt className="flex items-center gap-2">
                      <BrBadge br={br} />
                      <span className="text-muted-foreground truncate text-xs">{brTone[br].label}</span>
                    </dt>
                    <dd className={cn('text-sm', uniform ? 'text-muted-foreground' : 'font-medium')}>
                      {p.merge_policy[brLabel(br)] === 'auto' ? 'Auto-merge' : 'Human merge'}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          )
        })()}
      </Panel>

      <Card className="xl:col-span-2">
        <CardHeader><CardTitle>Gates</CardTitle><CardDescription className="ml-auto">Run in a separate gate sandbox, in order</CardDescription></CardHeader>
        <Table>
          <TableHeader>
            <TableRow><TableHead className="w-28">Gate</TableHead><TableHead>Command</TableHead><TableHead>Options</TableHead></TableRow>
          </TableHeader>
          <TableBody>
            {p.gates.map((g) => (
              <TableRow key={g.id}>
                <TableCell className="font-mono text-xs">{g.id}</TableCell>
                <TableCell className="font-mono text-xs">{g.run ?? <span className="text-muted-foreground">routes: {g.routes}</span>}</TableCell>
                <TableCell className="space-x-1">
                  {g.ratchet && <Badge variant="brand">ratchet</Badge>}
                  {g.max_new_duplication_pct !== undefined && <Badge>max new dupes {g.max_new_duplication_pct}%</Badge>}
                  {g.artifacts?.map((a) => <Badge key={a} variant="outline" className="font-mono">{a}</Badge>)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <Panel title="Blast radius layers" description="Highest matching layer wins">
        <ul className="space-y-2.5">
          {BRS.map((br) => (
            <li key={br} className="flex gap-3">
              <BrBadge br={br} className="mt-0.5" />
              <div className="flex min-w-0 flex-wrap gap-1">
                {p.layers[brLabel(br)].length ? p.layers[brLabel(br)].map((g) => <Code key={g}>{g}</Code>) : <span className="text-muted-foreground text-xs">No paths</span>}
              </div>
            </li>
          ))}
        </ul>
      </Panel>

      <Panel title="Protected paths" description="Agents never modify these">
        <ul className="flex flex-wrap gap-1.5">
          {p.protected_paths.map((path) => (
            <li key={path}><Code><Lock className="mr-1 inline size-3 align-[-2px]" aria-hidden />{path}</Code></li>
          ))}
        </ul>
      </Panel>

      <Panel title="Agent context" description="Rules, skills and MCP servers">
        <dl className="space-y-3 text-xs">
          <Row label="Rules files">{p.rules.files.map((f) => <Code key={f}>{f}</Code>)}</Row>
          <Row label="Skills">{p.rules.skills.length ? p.rules.skills.map((s) => <Code key={s}>{s}</Code>) : <span className="text-muted-foreground">None</span>}</Row>
          <Row label="MCP servers">
            {p.rules.mcp.length ? p.rules.mcp.map((m) => (
              <span key={m.name} className="flex items-center gap-1.5"><Code>{m.name}</Code><Badge variant="outline">{m.mode}</Badge></span>
            )) : <span className="text-muted-foreground">None</span>}
          </Row>
          <Row label="Models">{Object.entries(p.models).map(([role, model]) => <span key={role}><span className="text-muted-foreground">{role}</span> <Code>{model}</Code></span>)}</Row>
        </dl>
      </Panel>

      <Panel title="Environment" description="Sandbox build and synthetic seed">
        <dl className="space-y-3 text-xs">
          <Row label="Build"><Code>{p.environment.build}</Code></Row>
          <Row label="Install"><Code>{p.environment.install}</Code></Row>
          <Row label="Services">{p.environment.services.map((s) => <Code key={s}>{s}</Code>)}</Row>
          <Row label="Seed"><Code>{p.environment.seed}</Code></Row>
        </dl>
      </Panel>
    </div>
  )
}

function Panel({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader><CardTitle>{title}</CardTitle><CardDescription className="ml-auto">{description}</CardDescription></CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="text-lg font-semibold">{value}</dd>
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[96px_1fr] items-start gap-3">
      <dt className="text-muted-foreground pt-0.5">{label}</dt>
      <dd className="flex flex-wrap gap-1.5">{children}</dd>
    </div>
  )
}

function Code({ children }: { children: ReactNode }) {
  return <code className="bg-muted rounded-sm border px-1.5 py-0.5 font-mono text-xs">{children}</code>
}

/** Read-only YAML with line numbers and minimal token colouring (keys, comments, strings). */
function YamlView({ yaml }: { yaml: string }) {
  const lines = yaml.replace(/\n$/, '').split('\n')
  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <CardTitle className="font-mono text-xs">.alpine/factory.yaml</CardTitle>
        <CardDescription className="ml-auto tnum">{lines.length} lines</CardDescription>
      </CardHeader>
      <pre className="overflow-x-auto py-3 font-mono text-xs leading-5" tabIndex={0} aria-label="factory.yaml contents">
        {lines.map((line, i) => (
          <div key={i} className="hover:bg-muted/60 flex">
            <span className="tnum text-muted-foreground w-12 shrink-0 pr-4 text-right select-none" aria-hidden>{i + 1}</span>
            <span className="pr-4 whitespace-pre"><YamlLine line={line} /></span>
          </div>
        ))}
      </pre>
    </Card>
  )
}

function YamlLine({ line }: { line: string }) {
  const hashIdx = line.search(/\s#|^#/)
  const code = hashIdx >= 0 ? line.slice(0, hashIdx) : line
  const comment = hashIdx >= 0 ? line.slice(hashIdx) : ''
  const m = code.match(/^(\s*-?\s*)([\w.-]+)(:)(.*)$/)
  return (
    <>
      {m ? (
        <>
          {m[1]}<span className="text-foreground font-medium">{m[2]}</span>{m[3]}<Value text={m[4]} />
        </>
      ) : (
        <Value text={code} />
      )}
      {comment && <span className="text-muted-foreground italic">{comment}</span>}
    </>
  )
}

function Value({ text }: { text: string }) {
  const parts = text.split(/("[^"]*")/g)
  return (
    <>
      {parts.map((p, i) =>
        <span key={i} className="text-muted-foreground">{p}</span>,
      )}
    </>
  )
}
