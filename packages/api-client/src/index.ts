/**
 * Typed client for the Alpine API.
 *
 * `schema.gen.ts` is generated from `api/openapi.yaml` (`make gen` / `pnpm gen`) — do not edit it.
 * This file adds friendly type aliases, a fetch client and an SSE helper on top.
 */
import createClient, { type ClientOptions } from "openapi-fetch";

export type { Middleware } from "openapi-fetch";
import type { components, paths } from "./schema.gen.ts";

export type { components, paths, operations } from "./schema.gen.ts";

type Schemas = components["schemas"];
export type Health = Schemas["Health"];
export type AuthProviders = Schemas["AuthProviders"];
export type Me = Schemas["Me"];
export type User = Schemas["User"];
export type Org = Schemas["Org"];
export type OrgMembership = Schemas["OrgMembership"];
export type Role = Schemas["Role"];
export type Repo = Schemas["Repo"];
export type Stage = Schemas["Stage"];
export type TicketType = Schemas["TicketType"];
export type Priority = Schemas["Priority"];
export type Size = Schemas["Size"];
export type BlastRadius = Schemas["BlastRadius"];
export type Limits = Schemas["Limits"];
export type TicketSummary = Schemas["TicketSummary"];
export type TicketDetail = Schemas["TicketDetail"];
export type StageTransition = Schemas["StageTransition"];
export type AgentRun = Schemas["AgentRun"];
export type AgentTurn = Schemas["AgentTurn"];
export type GateResult = Schemas["GateResult"];
export type GateFinding = Schemas["GateFinding"];
export type GateStatus = Schemas["GateStatus"];
export type PullRequest = Schemas["PullRequest"];
export type PullRequestState = Schemas["PullRequestState"];
export type TicketAction = Schemas["TicketAction"];
export type BoardEvent = Schemas["BoardEvent"];
export type BoardEventType = Schemas["BoardEventType"];
export type ApiError = Schemas["Error"];
export type GateSummary = Schemas["GateSummary"];
export type DiffSummary = Schemas["DiffSummary"];
export type HumanDecision = Schemas["HumanDecision"];
export type Artifact = Schemas["Artifact"];
export type ReviewVerdict = Schemas["ReviewVerdict"];
export type ReviewFinding = Schemas["ReviewFinding"];
export type Review = Schemas["Review"];
export type VisualDiff = Schemas["VisualDiff"];
export type PullRequestFile = Schemas["PullRequestFile"];
export type PullRequestEvidence = Schemas["PullRequestEvidence"];
export type PullRequestReviewRequest = Schemas["PullRequestReviewRequest"];
export type MetricsWindow = Schemas["MetricsWindow"];
export type Metrics = Schemas["Metrics"];
export type MetricsTotals = Schemas["MetricsTotals"];
export type MetricsPoint = Schemas["MetricsPoint"];
export type GatePassRate = Schemas["GatePassRate"];
export type GateFailureReason = Schemas["GateFailureReason"];
export type FactoryConfigSpec = Schemas["FactoryConfigSpec"];
export type FactoryConfig = Schemas["FactoryConfig"];

/** Pipeline stages in board order (mirrors packages/shared/ticket.AllStages). */
export const STAGES: readonly Stage[] = [
  "intake",
  "triage",
  "refinement",
  "prioritize",
  "needs_human",
  "implement",
  "gates",
  "review",
  "merge_policy",
  "pr_open",
  "done",
  "escalated",
  "closed",
  "duplicate",
] as const;

/**
 * Create a typed API client. In the SPA, use the default `baseUrl: ""` with the Vite dev proxy
 * (or same-origin hosting) so the HTTP-only session cookie is sent.
 */
export function createAlpineClient(options: ClientOptions = {}) {
  return createClient<paths>({ credentials: "include", ...options });
}

export type AlpineClient = ReturnType<typeof createAlpineClient>;

/**
 * Subscribe to live board events (`GET /v1/events`, Server-Sent Events).
 * Returns an unsubscribe function.
 */
export function subscribeBoardEvents(
  onEvent: (event: BoardEvent) => void,
  opts: { baseUrl?: string; repoId?: string; onError?: (e: Event) => void } = {},
): () => void {
  const url = new URL(`${opts.baseUrl ?? ""}/v1/events`, globalThis.location?.origin ?? "http://localhost");
  if (opts.repoId) url.searchParams.set("repoId", opts.repoId);
  const source = new EventSource(url.toString(), { withCredentials: true });
  const types: BoardEventType[] = ["ticket.created", "ticket.stage_changed", "ticket.updated"];
  const handler = (msg: MessageEvent<string>) => {
    try {
      onEvent(JSON.parse(msg.data) as BoardEvent);
    } catch {
      /* ignore malformed frames */
    }
  };
  for (const t of types) source.addEventListener(t, handler as EventListener);
  if (opts.onError) source.onerror = opts.onError;
  return () => source.close();
}
