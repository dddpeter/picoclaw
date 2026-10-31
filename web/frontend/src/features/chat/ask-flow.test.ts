import { describe, expect, it } from "vitest"

import type { AskQuestion } from "@/store/chat"

import {
  type AskAnswer,
  OTHER,
  answerText,
  autoAdvance,
  buildSubmitBody,
  emptyAnswers,
  flattenLine,
} from "./ask-flow"

const q = (over: Partial<AskQuestion> = {}): AskQuestion => ({
  question: "Q?",
  ...over,
})

const optionsQ = q({
  question: "框架？",
  header: "框架",
  options: [
    { label: "React" },
    { label: "Vue", description: "渐进式" },
    { label: "Svelte" },
  ],
})

const picked = (...labels: string[]): AskAnswer => ({
  picked: [...labels],
  other: "",
})

describe("answerText", () => {
  it("resolves a picked option to its label", () => {
    expect(answerText(optionsQ, picked("Vue"))).toBe("Vue")
  })

  it("joins multi-select picks with '; '", () => {
    expect(
      answerText({ ...optionsQ, multiSelect: true }, picked("React", "Svelte")),
    ).toBe("React; Svelte")
  })

  it("returns just the free text when only 'other' is picked", () => {
    expect(answerText(optionsQ, { picked: [OTHER], other: " Solid " })).toBe(
      "Solid",
    )
  })

  it("returns null when 'other' is picked without text (评审 P2：不得视为已答)", () => {
    expect(answerText(optionsQ, { picked: [OTHER], other: "   " })).toBeNull()
  })

  it("appends 'other' text after concrete labels", () => {
    expect(
      answerText(optionsQ, { picked: ["React", OTHER], other: "都要" }),
    ).toBe("React; 都要")
  })

  it("returns the free text for open-ended questions", () => {
    expect(answerText(q(), { picked: [], other: "随便\n两行" })).toBe(
      "随便\n两行",
    )
  })

  it("returns null for an empty open-ended answer", () => {
    expect(answerText(q(), { picked: [], other: " " })).toBeNull()
  })

  it("returns null for an undefined answer (完成态降级)", () => {
    expect(answerText(optionsQ, undefined)).toBeNull()
  })
})

describe("flattenLine", () => {
  it("keeps a single line unchanged", () => {
    expect(flattenLine("一行答案")).toBe("一行答案")
  })

  it("joins multi-line text with '；'", () => {
    expect(flattenLine("方案A\n理由是…")).toBe("方案A；理由是…")
  })

  it("drops blank lines and trims each part", () => {
    expect(flattenLine(" a \n\n b \n")).toBe("a；b")
  })

  it("handles CRLF newlines", () => {
    expect(flattenLine("a\r\nb")).toBe("a；b")
  })
})

describe("buildSubmitBody", () => {
  it("returns null while any question is unanswered", () => {
    const questions = [optionsQ, q({ question: "原因？" })]
    const answers = [picked("React"), { picked: [], other: "" }]
    expect(buildSubmitBody(questions, answers)).toBeNull()
  })

  it("joins one answer per line in question order", () => {
    const questions = [optionsQ, q({ question: "原因？" })]
    const answers = [picked("Vue"), { picked: [], other: "快" }]
    expect(buildSubmitBody(questions, answers)).toBe("Vue\n快")
  })

  it("flattens embedded newlines in a multi-question card (评审 P1：防答案错位)", () => {
    const questions = [
      optionsQ,
      q({ question: "自由题" }),
      q({ question: "末题" }),
    ]
    const answers = [
      picked("React"),
      { picked: [], other: "方案A\n理由是…" },
      { picked: [], other: "好" },
    ]
    const body = buildSubmitBody(questions, answers)
    // 行数必须等于题数：后端 collectAskReply 行 i = 题 i。
    expect(body).toBe("React\n方案A；理由是…\n好")
    expect(body!.split("\n")).toHaveLength(3)
  })

  it("keeps newlines intact on a single-question card", () => {
    const questions = [q({ question: "详细说说" })]
    const answers: AskAnswer[] = [{ picked: [], other: "第一行\n第二行" }]
    expect(buildSubmitBody(questions, answers)).toBe("第一行\n第二行")
  })
})

describe("autoAdvance", () => {
  it("advances one step after a single-select preset pick", () => {
    expect(autoAdvance(0, 3, false, "React")).toEqual({
      mode: "step",
      current: 1,
    })
  })

  it("enters review after picking on the last question", () => {
    expect(autoAdvance(2, 3, false, "Vue")).toEqual({
      mode: "review",
      current: 2,
    })
  })

  it("enters review on a single-question card", () => {
    expect(autoAdvance(0, 1, false, "React")).toEqual({
      mode: "review",
      current: 0,
    })
  })

  it("does not advance when 'other' is picked (等自由文本)", () => {
    expect(autoAdvance(0, 3, false, OTHER)).toBeNull()
  })

  it("does not advance for multi-select (手动下一步)", () => {
    expect(autoAdvance(1, 3, true, "React")).toBeNull()
  })
})

describe("emptyAnswers", () => {
  it("creates one blank answer per question", () => {
    expect(emptyAnswers(3)).toEqual([
      { picked: [], other: "" },
      { picked: [], other: "" },
      { picked: [], other: "" },
    ])
  })
})
