import { createFileRoute } from "@tanstack/react-router"

import { FilesPage } from "@/components/files/files-page"

export const Route = createFileRoute("/files")({
  component: FilesPage,
})
