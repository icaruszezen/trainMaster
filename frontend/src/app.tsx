import { QueryClientProvider } from "@tanstack/react-query"
import { createBrowserRouter } from "react-router"
import { RouterProvider } from "react-router/dom"

import { ThemeProvider } from "@/components/theme-provider"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { queryClient } from "@/lib/query-client"
import { routes } from "@/router"

const router = createBrowserRouter(routes)

export function App() {
  return (
    <ThemeProvider storageKey="trainmaster-theme">
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <RouterProvider router={router} />
          <Toaster
            position="top-center"
            closeButton
            containerAriaLabel="通知"
            toastOptions={{ closeButtonAriaLabel: "关闭通知" }}
          />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
