import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { apiConfig } from '@/api/config'
import { ThemeProvider } from '@/design/theme'
import { createAppRouter } from '@/router'
import './index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, refetchOnWindowFocus: false, retry: 1 },
  },
})
const router = createAppRouter(queryClient)

async function bootstrap() {
  if (apiConfig.mode === 'mock') {
    const { startMocks } = await import('@/mocks')
    await startMocks()
  }
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ThemeProvider>
    </StrictMode>,
  )
}

void bootstrap()
