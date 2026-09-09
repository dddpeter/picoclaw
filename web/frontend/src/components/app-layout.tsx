import { useEffect, useState, type ReactNode } from "react"
import { Toaster } from "sonner"

import { AppHeader } from "@/components/app-header"
import { AppSidebar } from "@/components/app-sidebar"
import {
  getWizardDismissed,
  setWizardDismissed,
  SetupWizard,
  shouldAutoOpenWizard,
  type SetupWizardResult,
} from "@/components/setup/setup-wizard"
import { TourGuide } from "@/components/tour/tour-guide"
import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"
import { getModels } from "@/api/models"

function SetupWizardHost() {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    // Load once on mount: auto-open only for fresh installs with no usable
    // model (never dismissed). Skips silently for already-configured users.
    let cancelled = false
    ;(async () => {
      try {
        const data = await getModels()
        if (
          !cancelled &&
          shouldAutoOpenWizard(data.models || [], getWizardDismissed())
        ) {
          setOpen(true)
        }
      } catch {
        /* backend down — chat page shows its own error; stay quiet */
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const handleClose = (result?: SetupWizardResult) => {
    setOpen(false)
    setWizardDismissed(true)
    if (result?.providerAdded) {
      // reload so all pages pick up the new model + default
      window.location.reload()
    }
  }

  return <SetupWizard open={open} onClose={handleClose} />
}

export function AppLayout({ children }: { children: ReactNode }) {
  return (
    <TooltipProvider>
      <SidebarProvider className="flex h-dvh flex-col overflow-hidden">
        <AppHeader />

        <div className="flex flex-1 overflow-hidden">
          <AppSidebar />
          <div className="flex w-full flex-col overflow-hidden">
            <main className="flex min-h-0 w-full max-w-full flex-1 flex-col overflow-hidden">
              {children}
            </main>
          </div>
        </div>
        <Toaster position="bottom-center" />
        <TourGuide />
        <SetupWizardHost />
      </SidebarProvider>
    </TooltipProvider>
  )
}
