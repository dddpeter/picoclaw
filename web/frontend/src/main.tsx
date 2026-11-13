import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider, createRouter } from "@tanstack/react-router"
import { StrictMode } from "react"
import ReactDOM from "react-dom/client"

import { initA2uiBasicCatalog } from "@/components/chat/a2ui-card"

import { AppProviders } from "./app-providers"
import "./i18n"
import "./index.css"
import { routeTree } from "./routeTree.gen"

const queryClient = new QueryClient()

// A2UI basic catalog 一次性初始化（注册 custom elements + 注入样式，幂等）。
initA2uiBasicCatalog()

const router = createRouter({
  routeTree,
  context: {
    queryClient,
  },
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

const rootElement = document.getElementById("root")!
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <AppProviders>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </AppProviders>
    </StrictMode>,
  )
}
