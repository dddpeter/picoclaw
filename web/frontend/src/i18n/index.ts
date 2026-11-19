import dayjs from "dayjs"
import "dayjs/locale/bn"
import "dayjs/locale/cs"
import "dayjs/locale/en"
import "dayjs/locale/pt-br"
import "dayjs/locale/zh-cn"
import localizedFormat from "dayjs/plugin/localizedFormat"
import relativeTime from "dayjs/plugin/relativeTime"
import i18n from "i18next"
import LanguageDetector from "i18next-browser-languagedetector"
import { initReactI18next } from "react-i18next"

import en from "./locales/en.json"
import zh from "./locales/zh.json"

dayjs.extend(relativeTime)
dayjs.extend(localizedFormat)

// 入口 chunk 只内联 en + zh（默认语言；22 个组件测试依赖同步实例）。
// pt-BR / bn-IN / cs 三个语言包（合计 ~233KB）经 backend 在首次切换到
// 对应语言时动态 import——语言菜单行为不变，仅首次切换有一次加载延迟。
const lazyLanguages: Record<string, () => Promise<{ default: object }>> = {
  "pt-BR": () => import("./locales/pt-br.json"),
  "bn-IN": () => import("./locales/bn-in.json"),
  cs: () => import("./locales/cs.json"),
}

i18n
  // detect user language
  // learn more: https://github.com/i18next/i18next-browser-languageDetector
  .use(LanguageDetector)
  // 惰性语言包 backend：read 只对内联之外的语言触发（i18next 标准后端契约）。
  .use({
    type: "backend" as const,
    init() {},
    read(
      language: string,
      _namespace: string,
      callback: (err: unknown, data: unknown) => void,
    ) {
      const loader = lazyLanguages[language]
      if (!loader) {
        // en/zh 内联或未知语言：空数据走 resources/fallback。
        callback(null, {})
        return
      }
      loader()
        .then((mod) => callback(null, mod.default))
        .catch((err) => callback(err, null))
    },
  })
  // pass the i18n instance to react-i18next.
  .use(initReactI18next)
  // init i18next
  // for all options read: https://www.i18next.com/overview/configuration-options
  .init({
    resources: {
      en: {
        translation: en,
      },
      zh: {
        translation: zh,
      },
    },
    // init 不等待 backend（检测到惰性语言时后台补载，先以 fallback 渲染）。
    partialBundledLanguages: true,
    fallbackLng: "en",
    debug: false,

    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
  })

i18n.on("languageChanged", (lng) => {
  if (lng.startsWith("zh")) {
    dayjs.locale("zh-cn")
  } else if (lng.startsWith("pt")) {
    dayjs.locale("pt-br")
  } else if (lng.startsWith("bn")) {
    dayjs.locale("bn")
  } else if (lng.startsWith("cs")) {
    dayjs.locale("cs")
  } else {
    dayjs.locale("en")
  }
})

export default i18n
