import { ImageOff } from 'lucide-react'
import { useState } from 'react'
import type { VisualDiff } from '@/api/types'
import { Segmented, SegmentedItem } from '@/components/ui/segmented'

type Mode = 'side' | 'slider' | 'heatmap'

/** Before/after viewer: side by side, swipe slider, server-computed heatmap. */
export function VisualDiffViewer({ diff }: { diff: VisualDiff }) {
  const [mode, setMode] = useState<Mode>('side')
  const [pos, setPos] = useState(50)

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs">
          <span className="font-mono">{diff.route}</span>
          <span className="tnum text-muted-foreground"> · {diff.changedPixelsPct}% of pixels changed</span>
        </p>
        <Segmented value={mode} onValueChange={setMode} aria-label={`Diff view for ${diff.route}`}>
          <SegmentedItem value="side">Side by side</SegmentedItem>
          <SegmentedItem value="slider">Swipe</SegmentedItem>
          <SegmentedItem value="heatmap" disabled={!diff.heatmapUrl} title={diff.heatmapUrl ? undefined : 'Heatmap unavailable: change below 0.5%'}>
            Heatmap
          </SegmentedItem>
        </Segmented>
      </div>

      {mode === 'side' && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Shot label="Before · base" src={diff.beforeUrl} />
          <Shot label="After · branch" src={diff.afterUrl} />
        </div>
      )}

      {mode === 'slider' && (
        <div className="space-y-2">
          <div className="relative overflow-hidden rounded-md border">
            <img src={diff.afterUrl} alt={`After, ${diff.route}`} className="block w-full" />
            <div className="absolute inset-0" style={{ clipPath: `inset(0 ${100 - pos}% 0 0)` }}>
              <img src={diff.beforeUrl} alt={`Before, ${diff.route}`} className="block w-full" />
            </div>
            <div className="bg-brand absolute inset-y-0 w-px" style={{ left: `${pos}%` }} aria-hidden />
          </div>
          <input
            type="range"
            min={0}
            max={100}
            value={pos}
            onChange={(e) => setPos(Number(e.target.value))}
            className="accent-brand w-full"
            aria-label="Swipe between before and after"
          />
        </div>
      )}

      {mode === 'heatmap' && diff.heatmapUrl && <Shot label="Pixel diff heatmap" src={diff.heatmapUrl} />}
    </div>
  )
}

function Shot({ label, src }: { label: string; src: string }) {
  return (
    <figure className="space-y-1">
      <img src={src} alt={label} className="w-full rounded-md border" loading="lazy" />
      <figcaption className="text-muted-foreground text-2xs">{label}</figcaption>
    </figure>
  )
}

/** Empty before/after slots for changes with no affected routes (typical for BR3/BR4). */
export function VisualDiffSlot({ reason }: { reason: string }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {['Before · base', 'After · branch'].map((l) => (
        <figure key={l} className="space-y-1">
          <div className="text-muted-foreground flex aspect-[16/10] flex-col items-center justify-center gap-1.5 rounded-md border border-dashed text-2xs">
            <ImageOff className="size-4" aria-hidden />
            {reason}
          </div>
          <figcaption className="text-muted-foreground text-2xs">{l}</figcaption>
        </figure>
      ))}
    </div>
  )
}
