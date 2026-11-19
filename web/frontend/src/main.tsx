import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider, createRouter } from "@tanstack/react-router"
import { StrictMode } from "react"
import ReactDOM from "react-dom/client"

import { AppProviders } from "./app-providers"
import "./i18n"
import "./index.css"
import { routeTree } from "./routeTree.gen"

const queryClient = new QueryClient()

// A2UI basic catalog 初始化移到 a2ui-card 模块顶层（首个 render_ui 卡片
// 所在 chunk 才背 basic_catalog 的注册面），入口 chunk 不再静态引用该链。

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
