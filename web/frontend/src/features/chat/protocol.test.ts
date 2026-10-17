import { describe, expect, it } from "vitest";
import { parseContextUsage } from "@/features/chat/protocol";

// 分段解析（octop 借鉴 §五，评审 P3-8）：context_usage.segments 的 wire 解析
// 是 web 环分段堆叠条的数据源——畸形条目必须被丢弃而非炸掉整个环。
const wrap = (usage: Record<string, unknown>) => ({
	context_usage: usage,
});

describe("parseContextUsage segments", () => {
	it("parses valid segments and preserves order", () => {
		const usage = parseContextUsage(
			wrap({
				used_tokens: 1000,
				total_tokens: 4000,
				history_tokens: 400,
				segments: [
					{ key: "system_prompt", tokens: 300 },
					{ key: "conversation", tokens: 400 },
					{ key: "mcp", tokens: 300 },
				],
			}),
		);
		expect(usage?.segments).toEqual([
			{ key: "system_prompt", tokens: 300 },
			{ key: "conversation", tokens: 400 },
			{ key: "mcp", tokens: 300 },
		]);
	});

	it("drops malformed segment entries, keeps the valid ones", () => {
		const usage = parseContextUsage(
			wrap({
				used_tokens: 100,
				total_tokens: 400,
				segments: [
					{ key: "mcp", tokens: 100 },
					{ tokens: 50 }, // missing key
					{ key: "", tokens: 50 }, // empty key
					{ key: "skills", tokens: Number.NaN }, // NaN tokens
					"junk",
					null,
				],
			}),
		);
		expect(usage?.segments).toEqual([{ key: "mcp", tokens: 100 }]);
	});

	it("omits segments when absent, not an array, or fully malformed", () => {
		expect(
			parseContextUsage(wrap({ used_tokens: 1, total_tokens: 2 }))?.segments,
		).toBeUndefined();
		expect(
			parseContextUsage(wrap({ used_tokens: 1, total_tokens: 2, segments: "no" }))
				?.segments,
		).toBeUndefined();
		expect(
			parseContextUsage(wrap({ used_tokens: 1, total_tokens: 2, segments: [{}] }))
				?.segments,
		).toBeUndefined();
	});

	it("keeps zero-token entries (the ring filters them at render time)", () => {
		const usage = parseContextUsage(
			wrap({
				used_tokens: 50,
				total_tokens: 400,
				segments: [
					{ key: "skills", tokens: 0 },
					{ key: "conversation", tokens: 50 },
				],
			}),
		);
		expect(usage?.segments).toEqual([
			{ key: "skills", tokens: 0 },
			{ key: "conversation", tokens: 50 },
		]);
	});

	it("returns undefined without finite used/total", () => {
		expect(parseContextUsage(wrap({}))).toBeUndefined();
		expect(
			parseContextUsage(wrap({ used_tokens: 1, total_tokens: 0 })),
		).toBeUndefined();
		expect(
			parseContextUsage(wrap({ used_tokens: Number.NaN, total_tokens: 2 })),
		).toBeUndefined();
	});
});
