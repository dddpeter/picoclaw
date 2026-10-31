import { useAtomValue } from "jotai"

import {
  newChatSession,
  sendChatMessage,
  switchChatSession,
} from "@/features/chat/controller"
import { chatAtom } from "@/store/chat"

export function usePicoChat() {
  const {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    activeSessionChannel,
    contextUsage,
    turnStartedAt,
  } = useAtomValue(chatAtom)

  return {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    activeSessionChannel,
    contextUsage,
    turnStartedAt,
    sendMessage: sendChatMessage,
    switchSession: switchChatSession,
    newChat: newChatSession,
  }
}
