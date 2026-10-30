import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { createBrowserRouter } from "react-router"
import { RouterProvider } from "react-router/dom"

import { ThemeProvider } from "@/components/theme-provider"
import { TooltipProvider } from "@/components/ui/tooltip"
import { isPermanentError } from "@/lib/api"
import { routes } from "@/router"

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 网络错误、网关 5xx 可能是暂时的，重试一次；4xx / 501 重试也没用。
      retry: (failureCount, error) =>
        failureCount < 1 && !isPermanentError(error),
      staleTime: 10_000,
    },
  },
})

const router = createBrowserRouter(routes)

export function App() {
  return (
    <ThemeProvider storageKey="trainmaster-theme">
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <RouterProvider router={router} />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
