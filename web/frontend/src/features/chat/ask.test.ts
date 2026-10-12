import { describe, expect, it } from "vitest";
import {
	matchAskReceipt,
	parseAskFromPayload,
	parseAskQuestions,
	sealLastAsk,
} from "./ask";
import type { ChatMessage } from "@/store/chat";

describe("parseAskFromPayload / parseAskQuestions", () => {
	it("normalizes multi_select → multiSelect and filters empty labels", () => {
		const info = parseAskFromPayload({
			ask: {
				questions: [
					{
						question: "选组件",
						options: [{ label: "react" }, { label: " " }, { label: "vue" }],
						multi_select: true,
					},
					{ question: "  备注  ", header: " h " },
				],
				timeout_ms: 600000,
			},
		});
		expect(info).toBeDefined();
		expect(info!.questions).toHaveLength(2);
		expect(info!.questions[0].multiSelect).toBe(true);
		expect(info!.questions[0].options).toHaveLength(2);
		expect(info!.questions[1].header).toBe("h");
		expect(info!.timeoutMs).toBe(600000);
	});

	it("drops the payload when no question survives normalization", () => {
		expect(parseAskFromPayload({ ask: { questions: [{ question: "  " }] } })).toBeUndefined();
		expect(parseAskFromPayload({})).toBeUndefined();
	});

	it("parses restore-marker shaped raw questions the same way (P1-2)", () => {
		// The pending_ask marker stores bus.AskQuestion JSON verbatim
		// (multi_select, snake_case) — the restore path must ride the SAME
		// normalization or restored multi-select questions degrade to single.
		const restored = parseAskQuestions([
			{ question: "多选", options: [{ label: "a" }, { label: "b" }], multi_select: true },
			{ question: "plain" },
		]);
		expect(restored).toHaveLength(2);
		expect(restored[0].multiSelect).toBe(true);
	});
});

describe("matchAskReceipt", () => {
	it("maps the three receipt prefixes to seal verdicts", () => {
		expect(matchAskReceipt("📝 已收到回答，正在继续。")).toBe("answered");
		expect(matchAskReceipt("⏭️ 已跳过问询，继续。")).toBe("skipped");
		expect(matchAskReceipt("⏰ 问询超过 10m0s 未作答，按未作答处理，继续执行。")).toBe("done");
		expect(matchAskReceipt("❓ 当前没有待回答的问题。")).toBe("done");
		expect(matchAskReceipt("普通文本")).toBeUndefined();
	});
});

describe("sealLastAsk", () => {
	const base: ChatMessage = { id: "m1", role: "assistant", content: "", timestamp: 1 };
	const ask: ChatMessage = {
		...base,
		ask: { questions: [{ question: "q" }] },
	};

	it("seals the most recent unsealed ask card immutably", () => {
		const first = { ...ask, id: "a1" };
		const second = { ...ask, id: "a2" };
		const sealed = sealLastAsk([first, second], "skipped", 123);
		expect(sealed).toBeDefined();
		expect(sealed![0].askSeal).toBeUndefined();
		expect(sealed![1].askSeal).toEqual({ verdict: "skipped", at: 123 });
		// Immutability: the input array is untouched.
		expect(second.askSeal).toBeUndefined();
	});

	it("returns undefined when nothing is sealable", () => {
		expect(sealLastAsk([base], "answered", 1)).toBeUndefined();
	});
});
