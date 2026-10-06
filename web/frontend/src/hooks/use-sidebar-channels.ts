import {
  IconBrandChrome,
  IconBrandDingtalk,
  IconBrandDiscord,
  IconBrandLine,
  IconBrandMatrix,
  IconBrandQq,
  IconBrandSlack,
  IconBrandTelegram,
  IconBrandWechat,
  IconBrandWhatsapp,
  IconCamera,
  IconMessages,
  IconPlug,
  IconRobot,
} from "@tabler/icons-react"
import type { TFunction } from "i18next"
import { useAtomValue } from "jotai"
import * as React from "react"

import {
  type AppConfig,
  type SupportedChannel,
  getAppConfig,
  getChannelsCatalog,
} from "@/api/channels"
import { getChannelDisplayName } from "@/components/channels/channel-display-name"
import { gatewayAtom } from "@/store/gateway"

// 侧边栏平铺区最多直接展示的频道数；其余频道通过"全部频道"搜索面板访问。
const MAX_PINNED_CHANNELS = 8
const CHANNEL_IMPORTANCE_TAIL = [
  "slack",
  "line",
  "wecom",
  "dingtalk",
  "qq",
  "onebot",
  "matrix",
  "pico",
  "maixcam",
  "irc",
  "whatsapp",
  "whatsapp_native",
]

function getChannelImportanceOrder(language: string): string[] {
  const priority = language.startsWith("zh")
    ? ["feishu", "weixin", "discord", "telegram"]
    : ["discord", "telegram", "feishu", "weixin"]
  return [...priority, ...CHANNEL_IMPORTANCE_TAIL]
}

function IconLark({ className }: { className?: string }) {
  return React.createElement("span", {
    className,
    "aria-hidden": "true",
    style: {
      display: "inline-block",
      backgroundColor: "currentColor",
      mask: "url(/lark.svg) center / contain no-repeat",
      WebkitMask: "url(/lark.svg) center / contain no-repeat",
    } as React.CSSProperties,
  })
}

const CHANNEL_ICON_MAP: Record<
  string,
  React.ComponentType<{ className?: string }>
> = {
  telegram: IconBrandTelegram,
  discord: IconBrandDiscord,
  slack: IconBrandSlack,
  feishu: IconLark,
  dingtalk: IconBrandDingtalk,
  line: IconBrandLine,
  qq: IconBrandQq,
  weixin: IconBrandWechat,
  wecom: IconBrandWechat,
  whatsapp: IconBrandWhatsapp,
  whatsapp_native: IconBrandWhatsapp,
  matrix: IconBrandMatrix,
  maixcam: IconCamera,
  onebot: IconRobot,
  pico: IconBrandChrome,
  irc: IconMessages,
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

function isChannelEnabled(
  channel: SupportedChannel,
  channelsConfig: Record<string, unknown>,
): boolean {
  const channelConfig = asRecord(channelsConfig[channel.config_key])
  if (channelConfig.enabled !== true) {
    return false
  }

  // whatsapp / whatsapp_native share one config block and are split by use_native.
  if (channel.name === "whatsapp_native") {
    return channelConfig.use_native === true
  }
  if (channel.name === "whatsapp") {
    return channelConfig.use_native !== true
  }

  return true
}

function buildChannelEnabledMap(
  channels: SupportedChannel[],
  appConfig: AppConfig,
): Record<string, boolean> {
  // v3 配置的键是 channel_list；旧版（v1/v2）是 channels，保持兼容。
  const root = asRecord(appConfig)
  const channelsConfig = asRecord(
    root.channel_list !== undefined ? root.channel_list : root.channels,
  )
  const result: Record<string, boolean> = {}
  for (const channel of channels) {
    result[channel.name] = isChannelEnabled(channel, channelsConfig)
  }
  return result
}

export interface SidebarChannelNavItem {
  key: string
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  enabled: boolean
}

interface UseSidebarChannelsOptions {
  language: string
  t: TFunction
  /** 当前路由指向的频道名（/channels/<name>）；该频道即使未启用也保证平铺可见。 */
  activeChannelName?: string | null
}

export function useSidebarChannels({
  language,
  t,
  activeChannelName,
}: UseSidebarChannelsOptions) {
  const gateway = useAtomValue(gatewayAtom)
  const [channels, setChannels] = React.useState<SupportedChannel[]>([])
  const [enabledMap, setEnabledMap] = React.useState<Record<string, boolean>>(
    {},
  )

  const reloadChannels = React.useCallback((shouldApply?: () => boolean) => {
    Promise.all([
      getChannelsCatalog(),
      getAppConfig().catch(() => ({}) as AppConfig),
    ])
      .then(([catalog, appConfig]) => {
        if (shouldApply && !shouldApply()) {
          return
        }
        setChannels(catalog.channels)
        setEnabledMap(buildChannelEnabledMap(catalog.channels, appConfig))
      })
      .catch(() => {
        if (shouldApply && !shouldApply()) {
          return
        }
        setChannels([])
        setEnabledMap({})
      })
  }, [])

  React.useEffect(() => {
    let active = true
    reloadChannels(() => active)
    return () => {
      active = false
    }
  }, [reloadChannels])

  const previousGatewayStatusRef = React.useRef(gateway.status)
  React.useEffect(() => {
    const previousStatus = previousGatewayStatusRef.current
    if (previousStatus !== "running" && gateway.status === "running") {
      reloadChannels()
    }
    previousGatewayStatusRef.current = gateway.status
  }, [gateway.status, reloadChannels])

  const channelImportanceIndex = React.useMemo(() => {
    return new Map(
      getChannelImportanceOrder(language).map((name, index) => [name, index]),
    )
  }, [language])

  const sortedChannels = React.useMemo(() => {
    const list = [...channels]
    list.sort((a, b) => {
      const aEnabled = enabledMap[a.name] === true
      const bEnabled = enabledMap[b.name] === true
      if (aEnabled !== bEnabled) {
        return aEnabled ? -1 : 1
      }

      const aImportance =
        channelImportanceIndex.get(a.name) ?? Number.MAX_SAFE_INTEGER
      const bImportance =
        channelImportanceIndex.get(b.name) ?? Number.MAX_SAFE_INTEGER
      if (aImportance !== bImportance) {
        return aImportance - bImportance
      }

      return getChannelDisplayName(a, t).localeCompare(
        getChannelDisplayName(b, t),
      )
    })
    return list
  }, [channelImportanceIndex, channels, enabledMap, t])

  const toNavItem = React.useCallback(
    (channel: SupportedChannel): SidebarChannelNavItem => ({
      key: channel.name,
      title: getChannelDisplayName(channel, t),
      url: `/channels/${channel.name}`,
      icon: CHANNEL_ICON_MAP[channel.name] ?? IconPlug,
      enabled: enabledMap[channel.name] === true,
    }),
    [enabledMap, t],
  )

  // 平铺区 = 已启用的频道（截断到上限）+ 当前路由命中的频道（保证高亮可见）。
  const pinnedChannelItems = React.useMemo<SidebarChannelNavItem[]>(() => {
    const pinned = sortedChannels
      .filter((channel) => enabledMap[channel.name] === true)
      .slice(0, MAX_PINNED_CHANNELS)
      .map(toNavItem)

    if (activeChannelName) {
      const active = sortedChannels.find(
        (channel) => channel.name === activeChannelName,
      )
      if (active && !pinned.some((item) => item.key === active.name)) {
        if (pinned.length >= MAX_PINNED_CHANNELS) {
          pinned[pinned.length - 1] = toNavItem(active)
        } else {
          pinned.push(toNavItem(active))
        }
      }
    }

    return pinned
  }, [activeChannelName, enabledMap, sortedChannels, toNavItem])

  const allChannelItems = React.useMemo(
    () => sortedChannels.map(toNavItem),
    [sortedChannels, toNavItem],
  )

  return {
    pinnedChannelItems,
    allChannelItems,
  }
}
