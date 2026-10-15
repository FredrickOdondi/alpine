import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'

export function NotFoundPage() {
  return (
    <div className="flex min-h-[60vh] flex-col items-center justify-center gap-3 p-6 text-center">
      <p className="text-muted-foreground font-mono text-sm">404</p>
      <h1 className="text-xl font-semibold">Nothing on this trail.</h1>
      <Button asChild variant="outline">
        <Link to="/board">Back to the pipeline</Link>
      </Button>
    </div>
  )
}
