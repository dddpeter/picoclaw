interface ClipboardTextareaLike {
  value: string
  style: {
    position: string
    left: string
  }
  setAttribute(name: string, value: string): void
  select(): void
}

interface ClipboardBodyLike {
  appendChild(node: ClipboardTextareaLike): void
  removeChild(node: ClipboardTextareaLike): void
}

interface ClipboardDocumentLike {
  body: ClipboardBodyLike
  createElement(tagName: "textarea"): ClipboardTextareaLike
  execCommand(command: "copy"): boolean
}

interface ClipboardNavigatorLike {
  clipboard?: {
    writeText(text: string): Promise<void>
  }
}

export interface ClipboardEnvironment {
  document?: ClipboardDocumentLike
  navigator?: ClipboardNavigatorLike
}

function getDefaultClipboardEnvironment(): ClipboardEnvironment {
  return {
    document:
      typeof document === "undefined"
        ? undefined
        : (document as unknown as ClipboardDocumentLike),
    navigator:
      typeof navigator === "undefined"
        ? undefined
        : (navigator as unknown as ClipboardNavigatorLike),
  }
}

export async function copyText(
  text: string,
  environment: ClipboardEnvironment = getDefaultClipboardEnvironment(),
): Promise<boolean> {
  try {
    if (environment.navigator?.clipboard?.writeText) {
      await environment.navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // HTTP or restricted environments can reject Clipboard API writes.
  }

  if (!environment.document) {
    return false
  }

  // 契约：copyText **永不 reject**，成败只以 boolean 表达（评审 P1-1）。
  // 降级路径的 createElement/appendChild/select/removeChild 在受限环境
  // （非 HTTPS、受限 WebView、DOM 被策略限制）同样可能抛错；一旦外泄为
  // rejection，消费侧 `void copy(text).then(...)` 无从捕获，用户也拿不到
  // 失败提示（失败态本应由 resolve(false) 触发 toast）。
  let textArea: ClipboardTextareaLike | undefined
  try {
    textArea = environment.document.createElement("textarea")
    textArea.value = text
    textArea.setAttribute("readonly", "")
    textArea.style.position = "fixed"
    textArea.style.left = "-9999px"
    environment.document.body.appendChild(textArea)
    textArea.select()
    return environment.document.execCommand("copy")
  } catch {
    return false
  } finally {
    if (textArea) {
      try {
        environment.document.body.removeChild(textArea)
      } catch {
        // 清理失败不改变返回值的成败语义。
      }
    }
  }
}
