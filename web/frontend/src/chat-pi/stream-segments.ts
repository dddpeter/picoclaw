// 移植自 pi-web-ui（MIT License）web/src/stream-markdown.ts。
// 改名自 stream-markdown.ts：避免与组件 stream-markdown.tsx 的模块解析歧义
// （TS 在 ./stream-markdown 上优先命中 .ts）。
// SPDX-License-Identifier: MIT

/**
 * Streaming markdown segmentation — the core of the prefix-cache renderer.
 *
 * Problem: during streaming, the accumulated text changes on EVERY delta, so a
 * memoized full-document <Markdown text={...}> re-parses (remark → rehype →
 * highlight.js) the whole growing string per frame — O(n²) cumulative work
 * that visibly janks long code-heavy replies.
 *
 * Solution: split the text into FROZEN segments (complete, append-only blocks)
 * plus one ACTIVE tail. Frozen segment text never changes again, so each is
 * rendered by its own memoized <ReactMarkdown> exactly ONCE; only the small
 * active tail re-parses. At message end the parent does ONE authoritative
 * full-document parse, so any segmentation imperfection is transient.
 *
 * Splitting rules (CommonMark-aware enough for live preview):
 *  - Split points are blank lines OUTSIDE fenced code blocks.
 *  - An unclosed ```/~~~ fence keeps everything after its opener in the tail
 *    (half-rendered code as markdown would flash horribly on close).
 *  - A blank line followed by a list marker does NOT split: CommonMark treats
 *    blank-line-separated items as one (loose) list — splitting would restart
 *    ordered numbering at 1.
 *  - Setext headings / forward link-reference definitions across splits are
 *    accepted imperfections; the final full parse corrects them.
 */

export interface StreamSegments {
	/** Complete blocks — render once via memoized Markdown, keyed by index. */
	frozen: string[];
	/** The trailing (possibly incomplete) block — re-parsed as it grows. */
	active: string;
	/** True while `active` sits inside an unclosed fence — callers MUST NOT
	 *  syntax-highlight it (highlighting a growing block is O(n²) again). */
	inFence: boolean;
}

const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/;
const LIST_RE = /^\s{0,3}(?:[-*+]|\d{1,9}[.)])\s/;

function isBlank(line: string): boolean {
	return line.trim() === "";
}

/** Fence marker of a line, or null when the line does not open/close a fence. */
function fenceMarker(line: string): string | null {
	const m = FENCE_RE.exec(line);
	return m ? m[1] : null;
}

/**
 * Segment streaming markdown into frozen prefix + active tail.
 */
export function segmentStream(text: string): StreamSegments {
	const lines = text.split("\n");
	const frozen: string[] = [];
	let current: string[] = [];
	let fence: string | null = null; // opening marker of the open fence

	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];

		if (fence !== null) {
			// Inside a fence: everything belongs to the same segment until the
			// matching closer (same char, at least as long) appears.
			const marker = fenceMarker(line);
			if (marker && marker[0] === fence[0] && marker.length >= fence.length) {
				fence = null;
			}
			current.push(line);
			continue;
		}

		const marker = fenceMarker(line);
		if (marker && !isBlank(line)) {
			fence = marker;
			current.push(line);
			continue;
		}

		if (isBlank(line)) {
			// Candidate split point — BUT not when the next content line is a
			// list item (loose lists must stay in one segment).
			let j = i;
			while (j < lines.length && isBlank(lines[j])) j++;
			const nextIsList = j < lines.length && LIST_RE.test(lines[j]);
			if (!nextIsList && current.length > 0) {
				frozen.push(current.join("\n"));
				current = [];
				continue;
			}
			current.push(line);
			continue;
		}

		current.push(line);
	}

	return { frozen, active: current.join("\n"), inFence: fence !== null };
}
