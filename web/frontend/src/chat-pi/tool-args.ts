// 移植自 pi-web-ui（MIT License）web/src/tool-args.ts。
// SPDX-License-Identifier: MIT
// 适配点：去掉 delegate_task 专用解析（picoclaw 无该工具）。

/**
 * 工具卡头的「关键参数提示」纯函数：从 tool call 的 argumentsText 里安全取出
 * 文件路径 / 超时 / 命令行，供 ToolCallBlock 显示在卡头状态图标右侧。
 *
 * 为什么用正则扫描而不是 JSON.parse：
 *   1. 流式过程中 argumentsText 可能是**半截 JSON**——正则能在 `"path"` 一落地就显示，
 *      JSON.parse 只能等整串到齐；
 *   2. `write` 这类工具的参数里带着整个文件内容（可能几 MB），每次渲染 JSON.parse
 *      会明显卡顿（只扫描前 SCAN_LIMIT 字节）；
 *   3. AI 可能把参数填错（非 JSON / 类型不对 / 缺字段 / 超长 / 带换行），
 *      扫不到就静默返回空——不抛错、不显示半个值。
 */

/** 扫描上限：够覆盖正常工具参数，又不会因 write 的大 content 卡住渲染。 */
const SCAN_LIMIT = 256 * 1024;

/** 单值上限：AI 把整段说明塞进 path 时不至于撑爆卡头（超长截断）。 */
const VALUE_LIMIT = 300;

/** 路径类参数名。 */
const PATH_RE = /"(path|file_path|filePath|filename|file)"\s*:\s*"((?:[^"\\\n]|\\.){0,4000})"/;

const TIMEOUT_RE =
	/"(timeout|timeoutSeconds|timeout_seconds|timeoutSec|timeoutMs|timeout_ms|timeoutMilliseconds)"\s*:\s*(-?\d+(?:\.\d+)?)(?![0-9eE.])/;

/** 超过这个值（秒）的 timeout 视为脏数据，不显示。 */
const TIMEOUT_MAX = 1e9;

export interface ToolArgHints {
	/** 文件路径（读/写/编辑类工具），已剥离控制字符并按 VALUE_LIMIT 截断。 */
	path?: string;
	/** 超时提示文本，已带单位（"30s" / "1.5s" / "500ms"）。 */
	timeout?: string;
	/** bash 类工具的命令行（正文终端行用；保留原始换行）。 */
	command?: string;
}

/**
 * 从 argumentsText 提取卡头提示。任何异常输入（undefined / 空串 / 非 JSON /
 * 类型不对 / 半截 JSON / 超长）都只是少显示一个提示，绝不抛错。
 */
export function toolArgHints(argsText?: string): ToolArgHints {
	if (!argsText) return {};
	const text = argsText.length > SCAN_LIMIT ? argsText.slice(0, SCAN_LIMIT) : argsText;
	return {
		path: pathHint(text),
		timeout: timeoutHint(text),
		command: commandHint(argsText),
	};
}

/** 卡头显示用的路径压缩：保住文件名所在的尾段，前面用 …/ 省略。 */
export function shortenPath(path: string, max = 56): string {
	if (path.length <= max) return path;
	const parts = path.split(/[\\/]+/).filter(Boolean);
	const last = parts[parts.length - 1];
	if (!last || parts.length <= 1) return `…${path.slice(-(max - 1))}`;
	let out = last;
	for (let i = parts.length - 2; i >= 0; i--) {
		const next = `${parts[i]}/${out}`;
		if (next.length + 2 > max) break;
		out = next;
	}
	return `…/${out}`;
}

/* ------------------------------------------------------------------ */
/* internals                                                           */
/* ------------------------------------------------------------------ */

function pathHint(text: string): string | undefined {
	const m = PATH_RE.exec(text);
	if (!m) return undefined;
	const raw = decodeJsonString(m[2]);
	// 换行/制表等控制字符按空格归一（AI 误把整段说明或多行值填进 path 时）
	const cleaned = raw
		// eslint-disable-next-line no-control-regex -- 刻意清洗控制字符（AI 误填多行值进 path 时）
		.replace(/[\u0000-\u001f\u007f]/g, " ")
		.replace(/\s+/g, " ")
		.trim();
	if (!cleaned) return undefined;
	return cleaned.length > VALUE_LIMIT ? `${cleaned.slice(0, VALUE_LIMIT - 1)}…` : cleaned;
}

function timeoutHint(text: string): string | undefined {
	const m = TIMEOUT_RE.exec(text);
	if (!m) return undefined;
	const value = Number(m[2]);
	if (!Number.isFinite(value) || value <= 0 || value > TIMEOUT_MAX) return undefined;
	const key = m[1].toLowerCase();
	if (!key.endsWith("ms") && !key.endsWith("milliseconds")) return `${value}s`;
	if (value < 1000) return `${value}ms`;
	return `${Math.round(value / 100) / 10}s`;
}

/** bash 命令行：只有真能 JSON.parse 出 `command` 字符串时才给（保留原始换行）。 */
function commandHint(argsText: string): string | undefined {
	if (argsText.length > SCAN_LIMIT) return undefined;
	try {
		const parsed = JSON.parse(argsText) as { command?: unknown } | null;
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return undefined;
		return typeof parsed.command === "string" && parsed.command.trim() ? parsed.command : undefined;
	} catch {
		return undefined; // 半截 / 非法 JSON：正文回落 <pre> 原文
	}
}

/** 解码正则抓到的 JSON 字符串内容（\" \\ \n \uXXXX …）；转义不完整时原样返回。 */
function decodeJsonString(raw: string): string {
	try {
		const decoded: unknown = JSON.parse(`"${raw}"`);
		return typeof decoded === "string" ? decoded : raw;
	} catch {
		return raw;
	}
}
