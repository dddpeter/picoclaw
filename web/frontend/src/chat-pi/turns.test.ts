import { describe, expect, it } from "vitest";
import type { ChatMessage } from "@/store/chat";
import {
	buildTurns,
	isLikelyToolError,
	parseTimestampMs,
	resetFeedbackObservations,
	type PiTurn,
} from "./turns";

function user(id: string, content = "hi", extra: Partial<ChatMessage> = {}): ChatMessage {
	return { id, role: "user", content, timestamp: 1_700_000_000, ...extra };
}

function assistant(
	id: string,
	content = "",
	extra: Partial<ChatMessage> = {},
): ChatMessage {
	return { id, role: "assistant", content, timestamp: 1_700_000_001, ...extra };
}

function toolMessage(
	id: string,
	toolId: string,
	opts: { name?: string; args?: string; feedback?: string } = {},
): ChatMessage {
	return assistant(id, "", {
		kind: "tool_calls",
		toolCalls: [
			{
				id: toolId,
				function: {
					...(opts.name ? { name: opts.name } : {}),
					...(opts.args ? { arguments: opts.args } : {}),
				},
				...(opts.feedback === undefined
					? {}
					: { extraContent: { toolFeedbackExplanation: opts.feedback } }),
			},
		],
	});
}

function blockTypes(turns: PiTurn[]): string[] {
	return (turns[0].assistant?.blocks ?? []).map((block) => block.type);
}

function feedbackEndedAt(turns: PiTurn[]): number | undefined {
	const block = turns[0].assistant?.blocks.find((candidate) => candidate.type === "toolCall");
	return block?.type === "toolCall" ? block.feedback?.endedAt : undefined;
}

const ALL = { isTyping: false, detail: "all" } as const;

describe("buildTurns", () => {
	it("merges a single user/assistant pair into one turn", () => {
		const turns = buildTurns(
			[user("u1"), assistant("a1", "hello", { modelName: "gpt" })],
			{ ...ALL, now: 123 },
		);

		expect(turns).toHaveLength(1);
		expect(turns[0]).toMatchObject({ id: "u1", active: false });
		// 秒级时间戳在 turn 上统一升为毫秒。
		expect(turns[0].user).toMatchObject({ text: "hi", timestamp: 1_700_000_000_000 });
		expect(turns[0].assistant?.modelName).toBe("gpt");
		expect(turns[0].assistant?.blocks).toEqual([
			{ type: "text", id: "a1", text: "hello", live: false },
		]);
	});

	it("opens a separate turn for steering messages", () => {
		const turns = buildTurns(
			[
				user("u1", "start"),
				assistant("a1", "working"),
				user("u2", "wait", { steering: true }),
				assistant("a2", "ok"),
			],
			ALL,
		);

		expect(turns.map((turn) => turn.id)).toEqual(["u1", "u2"]);
		expect(turns[1].user?.steering).toBe(true);
		// 插问后的助手块归入插问自己的 turn。
		expect(turns[1].assistant?.blocks).toEqual([
			{ type: "text", id: "a2", text: "ok", live: false },
		]);
	});

	it("synthesises a head-less turn when history starts mid-conversation", () => {
		const turns = buildTurns([assistant("a1", "half")], ALL);

		expect(turns).toHaveLength(1);
		expect(turns[0].id).toBe("head-a1");
		expect(turns[0].user).toBeUndefined();
		expect(turns[0].assistant?.blocks[0]).toMatchObject({ type: "text", text: "half" });
	});

	it("merges tool calls with their feedback and flags errors", () => {
		resetFeedbackObservations();
		const turns = buildTurns(
			[user("u1"), toolMessage("a1", "c1", { name: "exec", args: '{"cmd":"ls"}', feedback: "error: boom" })],
			{ ...ALL, now: 999 },
		);

		expect(turns[0].assistant?.blocks).toEqual([
			{
				type: "toolCall",
				id: "a1#c1",
				name: "exec",
				argumentsText: '{"cmd":"ls"}',
				startedAt: 1_700_000_001_000,
				feedback: { text: "error: boom", isError: true, endedAt: 999 },
			},
		]);
	});

	it("leaves feedback undefined until an explanation arrives", () => {
		const turns = buildTurns([user("u1"), toolMessage("a1", "c1", { name: "read" })], ALL);

		expect(turns[0].assistant?.blocks[0]).not.toHaveProperty("feedback");
		expect(turns[0].assistant?.blocks[0]).toMatchObject({ type: "toolCall", name: "read" });
	});

	it("filters thought and tool blocks by detail visibility", () => {
		const messages = [
			user("u1"),
			assistant("a1", "thinking", { kind: "thought" }),
			toolMessage("a2", "c1", { name: "exec" }),
			assistant("a3", "answer"),
		];

		expect(blockTypes(buildTurns(messages, { isTyping: false, detail: "none" }))).toEqual(["text"]);
		expect(blockTypes(buildTurns(messages, { isTyping: false, detail: "thought" }))).toEqual([
			"thinking",
			"text",
		]);
		expect(blockTypes(buildTurns(messages, { isTyping: false, detail: "tool_calls" }))).toEqual([
			"toolCall",
			"text",
		]);
		expect(blockTypes(buildTurns(messages, ALL))).toEqual(["thinking", "toolCall", "text"]);
	});
});

describe("streaming state", () => {
	it("adds a pending assistant card while the turn is typing", () => {
		const turns = buildTurns([user("u1")], { isTyping: true, detail: "all", now: 42 });

		expect(turns).toHaveLength(1);
		expect(turns[0].active).toBe(true);
		expect(turns[0].assistant).toEqual({
			id: "u1#pending",
			timestamp: 1_700_000_000_000,
			blocks: [],
		});
	});

	it("creates a synthetic pending turn for an empty stream", () => {
		const turns = buildTurns([], { isTyping: true, detail: "all", now: 42 });

		expect(turns).toHaveLength(1);
		expect(turns[0]).toMatchObject({ id: "pending", active: true });
		expect(turns[0].assistant?.blocks).toEqual([]);
	});

	it("marks the trailing assistant message live and earlier ones cold", () => {
		const live = buildTurns([user("u1"), assistant("a1", "part")], {
			isTyping: true,
			detail: "all",
			now: 1,
		});
		expect(live[0].assistant?.blocks[0]).toMatchObject({ live: true });

		const cold = buildTurns([user("u1"), assistant("a1", "part"), user("u2")], {
			isTyping: true,
			detail: "all",
			now: 1,
		});
		expect(cold[0].assistant?.blocks[0]).toMatchObject({ live: false });
		expect(cold[1].active).toBe(true);
	});
});

describe("parseTimestampMs", () => {
	it("accepts seconds, milliseconds and numeric strings", () => {
		expect(parseTimestampMs(1_700_000_000)).toBe(1_700_000_000_000);
		expect(parseTimestampMs(1_700_000_000_000)).toBe(1_700_000_000_000);
		expect(parseTimestampMs("1700000000")).toBe(1_700_000_000_000);
		expect(parseTimestampMs("1700000000000")).toBe(1_700_000_000_000);
		expect(parseTimestampMs(" 1700000000 ")).toBe(1_700_000_000_000);
	});

	it("falls back to Date.parse for ISO strings", () => {
		const iso = "2024-01-02T03:04:05Z";
		expect(parseTimestampMs(iso)).toBe(Date.parse(iso));
	});

	it("returns 0 for unparsable values", () => {
		expect(parseTimestampMs("nonsense")).toBe(0);
		expect(parseTimestampMs("")).toBe(0);
	});
});

describe("isLikelyToolError", () => {
	it("detects error prefixes and non-zero exit codes", () => {
		expect(isLikelyToolError("error: boom")).toBe(true);
		expect(isLikelyToolError("\n  Failed to run")).toBe(true);
		expect(isLikelyToolError("⛔ blocked")).toBe(true);
		expect(isLikelyToolError("done\nexit code 2")).toBe(true);
		expect(isLikelyToolError("all good")).toBe(false);
		expect(isLikelyToolError("exit code 0")).toBe(false);
	});
});

describe("feedback observation registry", () => {
	it("evicts the oldest observation once the cap is reached", () => {
		resetFeedbackObservations();
		const observed = (id: string, now: number) =>
			feedbackEndedAt(buildTurns([toolMessage(id, "c1", { feedback: "ok" })], { ...ALL, now }));

		expect(observed("first", 1000)).toBe(1000);

		// 越过 500 条上界后，最早的观测应被淘汰并按新时刻重新登记。
		for (let i = 0; i < 600; i += 1) {
			buildTurns([toolMessage(`filler-${i}`, "c1", { feedback: "ok" })], { ...ALL, now: 1100 });
		}

		expect(observed("first", 2000)).toBe(2000);
	});

	it("keeps observed instants stable for repeated renders", () => {
		resetFeedbackObservations();
		const messages = [toolMessage("a1", "c1", { feedback: "ok" })];

		expect(feedbackEndedAt(buildTurns(messages, { ...ALL, now: 111 }))).toBe(111);
		expect(feedbackEndedAt(buildTurns(messages, { ...ALL, now: 222 }))).toBe(111);
	});
});

describe("buildTurns 卡片 latest 门控", () => {
	const approvalMsg = (id: string, extra: Partial<ChatMessage> = {}): ChatMessage =>
		assistant(id, "⚠️ 需要批准：即将执行工具 shell", {
			approval: { tool: "shell" },
			...extra,
		});
	const askMsg = (id: string, extra: Partial<ChatMessage> = {}): ChatMessage =>
		assistant(id, "", { ask: { questions: [{ question: "q" }] }, ...extra });

	it("marks only the newest unsealed card of each type as latest", () => {
		const turns = buildTurns(
			[
				user("u1"),
				approvalMsg("a1"),
				approvalMsg("a2", { approvalSeal: { verdict: "approved", at: 2 } }),
				askMsg("k1"),
				askMsg("k2"),
			],
			ALL,
		);
		const blocks = turns.flatMap((t) => t.assistant?.blocks ?? []);
		const approvalBlocks = blocks.filter((b) => b.type === "approval");
		const askBlocks = blocks.filter((b) => b.type === "ask");
		// a1 是唯一未封存审批卡 → latest；a2 已封存不参与。
		expect(approvalBlocks[0].latest).toBe(true);
		// 两张未封存 ask 卡：只有较新的 k2 是 latest，旧的 k2 之前那张保持
		// undefined（组件默认 false → 按钮禁用，防提交错位）。
		expect(askBlocks[0].latest).toBeUndefined();
		expect(askBlocks[1].latest).toBe(true);
	});

	it("leaves stale ghost cards non-actionable when a fresh card exists", () => {
		// 超时幽灵卡（未封存）+ 新卡并存：旧卡必须不可操作。
		const turns = buildTurns([user("u1"), askMsg("ghost"), askMsg("fresh")], ALL);
		const askBlocks = turns.flatMap((t) => t.assistant?.blocks ?? []).filter((b) => b.type === "ask");
		expect(askBlocks[0].latest).toBeUndefined();
		expect(askBlocks[1].latest).toBe(true);
	});
});
