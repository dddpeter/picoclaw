import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type PluginItem,
  type PluginReportResponse,
  getPlugins,
  installPlugin,
  removePlugin,
  setPluginEnabled,
  validatePlugin,
} from "@/api/plugins"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

export function usePluginsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const pluginsQuery = useQuery({
    queryKey: ["plugins"],
    queryFn: getPlugins,
  })

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["plugins"] })

  const notifyRestartIfRequired = async (savedMessage: string) => {
    const gateway = await refreshGatewayState({ force: true })
    showSaveSuccessOrRestartToast(
      t,
      savedMessage,
      t("navigation.plugins", "Plugins"),
      gateway?.restartRequired === true,
    )
  }

  const toggleMutation = useMutation({
    mutationFn: ({
      name,
      enabled,
    }: {
      name: string
      enabled: boolean
    }) => setPluginEnabled(name, enabled),
    onSuccess: async (_, variables) => {
      await notifyRestartIfRequired(
        variables.enabled
          ? t("pages.plugins.enabled_toast", "Plugin enabled")
          : t("pages.plugins.disabled_toast", "Plugin disabled"),
      )
      await invalidate()
    },
    onError: (error: Error) => {
      toast.error(error.message)
    },
  })

  const removeMutation = useMutation({
    mutationFn: ({ name, purgeData }: { name: string; purgeData: boolean }) =>
      removePlugin(name, purgeData),
    onSuccess: async (_, variables) => {
      await notifyRestartIfRequired(
        t("pages.plugins.removed_toast", "Plugin removed"),
      )
      void variables
      await invalidate()
    },
    onError: (error: Error) => {
      toast.error(error.message)
    },
  })

  const installMutation = useMutation({
    mutationFn: ({ source, ref }: { source: string; ref: string }) =>
      installPlugin(source, ref),
    onSuccess: async (report: PluginReportResponse) => {
      if (report.error) {
        toast.warning(
          t("pages.plugins.installed_with_errors", "Installed with problems"),
          { description: report.error },
        )
      } else {
        await notifyRestartIfRequired(
          t("pages.plugins.installed_toast", "Plugin installed"),
        )
      }
      await invalidate()
    },
    onError: (error: Error) => {
      toast.error(error.message)
    },
  })

  const validateMutation = useMutation({
    mutationFn: ({ path }: { path: string }) => validatePlugin(path),
    onError: (error: Error) => {
      toast.error(error.message)
    },
  })

  // Install dialog state
  const [installOpen, setInstallOpen] = useState(false)
  const [sourceText, setSourceText] = useState("")
  const [refText, setRefText] = useState("")

  // Remove confirmation state
  const [confirmRemove, setConfirmRemove] = useState<PluginItem | null>(null)
  const [purgeData, setPurgeData] = useState(false)

  const openInstall = () => {
    setSourceText("")
    setRefText("")
    validateMutation.reset()
    setInstallOpen(true)
  }

  return {
    pluginsQuery,
    plugins: pluginsQuery.data?.plugins ?? [],
    installRoot: pluginsQuery.data?.installRoot ?? "",
    registryError: pluginsQuery.data?.registryError ?? "",
    toggleMutation,
    removeMutation,
    installMutation,
    validateMutation,
    installOpen,
    setInstallOpen,
    sourceText,
    setSourceText,
    refText,
    setRefText,
    openInstall,
    confirmRemove,
    setConfirmRemove,
    purgeData,
    setPurgeData,
  }
}
