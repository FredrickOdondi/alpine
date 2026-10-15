import { parse } from 'yaml'
import type { FactoryConfig, FactoryConfigSpec } from '@/api/types'
import { PAYMENTS as PAYMENTS_ID, STOREFRONT as STOREFRONT_ID } from './db'

const STOREFRONT = `version: 1
rules:
  files: [AGENTS.md, CLAUDE.md, .alpine/rules/*.md]   # concatenated, in order
  skills: [vercel/react, aws/lambda]                  # vendor skill packs
  mcp:
    - name: posthog
      url: https://mcp.example.com/posthog
      mode: read-only
environment:
  build: .alpine/env/Dockerfile
  install: pnpm install --frozen-lockfile
  services: [postgres:16, redis:7]
  seed: .alpine/seed/seed.ts        # synthetic/anonymized data recipe — never prod dumps
layers:                             # blast radius mapping (highest match wins)
  BR4: ["db/migrations/**", "prisma/schema.prisma", "infra/**", "src/billing/**"]
  BR3: ["src/server/**", "api/**"]
  BR2: ["src/design-system/**", "src/components/ui/**"]
  BR1: ["src/pages/**", "src/features/**"]
protected_paths: [".github/workflows/**", ".alpine/**", "src/auth/**"]
gates:
  - id: lint
    run: pnpm lint
  - id: typecheck
    run: pnpm tsc --noEmit
  - id: fallow
    run: npx fallow health --format json
    ratchet: true                 # new code must not worsen the base-branch baseline
  - id: dupes
    run: npx jscpd --reporters json src
    max_new_duplication_pct: 0
  - id: unit
    run: pnpm test --reporter=json
  - id: e2e
    run: pnpm playwright test --grep @affected
    artifacts: ["test-results/**"]
  - id: visual
    routes: auto                  # derive affected routes from diff + ticket
limits:
  max_turns: 100
  max_gate_iterations: 15
  max_cost_usd: 20
  max_parallel_tickets: 5
models:
  implementer: strong
  reviewer: strong-alt-family
  triage: cheap
merge_policy:
  BR1: human        # later: auto
  BR2: human
  BR3: human
  BR4: human
`

const PAYMENTS = `version: 1
rules:
  files: [AGENTS.md, .alpine/rules/*.md]
  skills: [aws/lambda]
  mcp: []
environment:
  build: .devcontainer/devcontainer.json
  install: go mod download
  services: [postgres:16]
  seed: .alpine/seed/seed.sql
layers:
  BR4: ["migrations/**", "internal/ledger/**", "internal/payouts/**", "infra/**"]
  BR3: ["internal/**", "api/**"]
  BR2: []
  BR1: ["docs/**"]
protected_paths: [".github/workflows/**", ".alpine/**", "internal/auth/**", "migrations/**"]
gates:
  - id: lint
    run: golangci-lint run ./...
  - id: typecheck
    run: go vet ./...
  - id: dupes
    run: npx jscpd --reporters json internal
    max_new_duplication_pct: 0
  - id: unit
    run: go test ./... -json
limits:
  max_turns: 60
  max_gate_iterations: 10
  max_cost_usd: 12
  max_parallel_tickets: 2
models:
  implementer: strong
  reviewer: strong-alt-family
  triage: cheap
merge_policy:
  BR1: human
  BR2: human
  BR3: human
  BR4: human
`

const RAW: Record<string, { yaml: string; sha: string }> = {
  [STOREFRONT_ID]: { yaml: STOREFRONT, sha: '4f2c9e1' },
  [PAYMENTS_ID]: { yaml: PAYMENTS, sha: 'b81d07a' },
}

export function factoryConfigFor(repoId: string): FactoryConfig | null {
  const entry = RAW[repoId]
  if (!entry) return null
  return {
    repoId,
    path: '.alpine/factory.yaml',
    sha: entry.sha,
    rawYaml: entry.yaml,
    parsed: parse(entry.yaml) as FactoryConfigSpec,
    validation: { valid: true, errors: [] },
    updatedAt: new Date(Date.now() - 3 * 86_400_000).toISOString(),
  }
}
