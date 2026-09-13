import { createFileRoute } from "@tanstack/react-router"

import { MCPPage } from "@/components/mcp/mcp-page"

export const Route = createFileRoute("/mcp")({
  component: MCPRoute,
})

function MCPRoute() {
  return <MCPPage />
}
