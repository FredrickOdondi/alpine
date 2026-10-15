import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRouter } from '@tanstack/react-router'
import { FlaskConical, LoaderCircle } from 'lucide-react'
import { useState } from 'react'
import { api } from '@/api/client'
import { apiConfig } from '@/api/config'
import { authProvidersQuery, queryKeys } from '@/api/queries'
import type { BlastRadius, BoardColumn } from '@/api/types'
import { BrBadge } from '@/components/app/badges'
import { GitHubMark } from '@/components/app/github-mark'
import { AlpineMark } from '@/components/app/logo'
import { Button } from '@/components/ui/button'
import { stageTone } from '@/design/tokens'
import { cn } from '@/lib/utils'

export function LoginPage({ redirectTo }: { redirectTo: string }) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const qc = useQueryClient()
  const router = useRouter()

  const providers = useQuery(authProvidersQuery)
  const target = redirectTo.startsWith('/') ? redirectTo : '/board'

  /** POST /v1/auth/dev-login sets the session cookie; then load the app. */
  const devLogin = async () => {
    setPending(true)
    setError(null)
    try {
      await api.devLogin()
      await qc.invalidateQueries({ queryKey: queryKeys.me })
      router.history.push(target)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Sign-in failed. Try again.')
      setPending(false)
    }
  }

  // Live: full-page redirect into GitHub OAuth. Mock: the round-trip is simulated with dev-login.
  const signIn = () => {
    if (apiConfig.mode === 'mock') return devLogin()
    setPending(true)
    api.startGithubLogin(target) // spec: redirect_to is an SPA path starting with "/"
  }
  const googleSignIn = () => {
    setPending(true)
    api.startGoogleLogin(target)
  }
  const showGoogle = apiConfig.mode === 'live' && providers.data?.google
  const showDevLogin = apiConfig.mode === 'live' && providers.data?.devLogin

  return (
    <div className="grid min-h-screen lg:grid-cols-[440px_1fr]">
      <main className="flex flex-col px-10 py-8">
        <div className="flex items-center gap-2">
          <AlpineMark />
          <span className="text-sm font-medium">Alpine</span>
        </div>
        <div className="my-auto w-full max-w-xs space-y-6 py-12">
          <div className="space-y-1">
            <h1 className="text-xl font-semibold tracking-tight">Sign in to Alpine</h1>
            <p className="text-muted-foreground">Continue with the GitHub account that belongs to your organization.</p>
          </div>
          <Button size="lg" className="w-full" onClick={signIn} disabled={pending || providers.data?.github === false} aria-busy={pending}>
            {pending ? <LoaderCircle className="animate-spin" /> : <GitHubMark />}
            {pending ? 'Waiting for GitHub…' : 'Sign in with GitHub'}
          </Button>
          {showGoogle && (
            <Button variant="outline" className="w-full" onClick={googleSignIn} disabled={pending}>
              Continue with Google
            </Button>
          )}
          {showDevLogin && (
            <Button variant="outline" className="w-full" onClick={devLogin} disabled={pending}>
              Continue with dev login
            </Button>
          )}
          {error && (
            <p role="alert" className="text-fail text-xs">{error}</p>
          )}
          <p className="text-muted-foreground text-xs leading-relaxed">
            Sign-in reads your profile and org membership only. Repository access is granted separately when an admin
            installs the Alpine GitHub App.
          </p>
          {apiConfig.mode === 'mock' && (
            <p className="bg-muted text-muted-foreground flex items-start gap-2 rounded-md px-3 py-2 text-xs">
              <FlaskConical className="mt-px size-4 shrink-0" aria-hidden />
              Mock API: the OAuth round-trip is simulated and signs you in as a demo user.
            </p>
          )}
        </div>
        <p className="text-muted-foreground text-xs">Alpine · agentic software factory</p>
      </main>
      <PipelinePreview />
    </div>
  )
}

const PREVIEW: { stage: BoardColumn; key: string; title: string; br: BlastRadius; meta: string }[] = [
  { stage: 'refinement', key: 'SHOP-142', title: 'Footer links keep the old locale prefix', br: 1, meta: 'Reproducing · 3 screenshots' },
  { stage: 'implement', key: 'PAY-133', title: 'Retry storm when card network times out', br: 3, meta: 'Turn 41 / 100 · $3.12' },
  { stage: 'gates', key: 'SHOP-123', title: 'Product grid jumps when images lazy-load', br: 1, meta: 'e2e running · iteration 2' },
  { stage: 'merge', key: 'SHOP-125', title: 'Button ignores size="sm" on dark surfaces', br: 2, meta: 'PR #426 · gates 7/7' },
]

/** Static, decorative preview of the board. Hidden from assistive tech. */
function PipelinePreview() {
  return (
    <aside className="bg-muted/60 hidden flex-col justify-center border-l px-12 lg:flex" aria-hidden>
      <div className="w-full max-w-3xl space-y-3">
        <p className="max-w-xl text-base">From bug report to verified merge, with deterministic gates and a merge policy that knows what is risky.</p>
        <div className="bg-card divide-y rounded-lg border">
          {PREVIEW.map((row) => (
            <div key={row.key} className="flex items-center gap-3 px-4 py-3">
              <span className={cn('inline-block size-2 shrink-0 rounded-full', stageTone[row.stage].dot)} />
              <span className="text-muted-foreground w-18 shrink-0 text-xs">{stageTone[row.stage].label}</span>
              <span className="text-muted-foreground w-16 shrink-0 font-mono text-xs">{row.key}</span>
              <span className="min-w-0 flex-1 truncate">{row.title}</span>
              <BrBadge br={row.br} />
              <span className="tnum text-muted-foreground shrink-0 text-right text-xs whitespace-nowrap">{row.meta}</span>
            </div>
          ))}
        </div>
      </div>
    </aside>
  )
}
