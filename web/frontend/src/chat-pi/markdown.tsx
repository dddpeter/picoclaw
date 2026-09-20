// 移植自 pi-web-ui（MIT License）web/src/components/Markdown.tsx。
// SPDX-License-Identifier: MIT
// 适配点：去掉插件围栏（plugin-fence）与 mermaid 宿主分支，pre 一律走普通代码块；
// KaTeX / 行号 / 复制按钮 / 外链处理与原版一致。
import { memo, type ReactNode } from "react";
import type { JSX } from "react";
import ReactMarkdown from "react-markdown";
import type { PluggableList } from "unified";
import remarkGfm from "remark-gfm";
import remarkBreaks from "remark-breaks";
import remarkMath from "remark-math";
import rehypeHighlight from "rehype-highlight";
import rehypeKatex from "rehype-katex";
import rehypeRaw from "rehype-raw";
import "katex/dist/katex.min.css";
import { CopyButton } from "./copy-button";
import { splitCodeLines } from "./code-lines";

interface MarkdownProps {
	text: string;
	/** 渲染原始 HTML（嵌在 markdown 里）。默认关闭：聊天消息的 markdown 镜像会
	 *  转义 HTML，信任模型的地方可开启以支持 HTML + markdown 混排。 */
	rawHtml?: boolean;
	/** 保留单个换行（\n → <br>）。用户气泡开启以忠实呈现用户原文的换行。 */
	hardBreaks?: boolean;
}

/** Shared markdown pipeline + codeblock chrome (copy button). Exported so
 *  StreamMarkdown's per-segment renderers reuse the exact same configuration
 *  as this full-document renderer — streaming preview and final render must
 *  be visually identical. */
export const remarkPlugins = [remarkGfm, remarkMath];
/** Same pipeline + hard line breaks — used for USER bubbles so typed/pasted
 *  multi-line text keeps every line break (see MarkdownProps.hardBreaks). */
export const remarkPluginsHardBreaks = [remarkGfm, remarkBreaks, remarkMath];
export const rehypePlugins: PluggableList = [
	// KaTeX 在 highlight 之前：两者目标节点不相交（.math vs pre code），
	// 公式解析失败时只显示红色源码（throwOnError: false），不打断整条消息。
	[rehypeKatex, { strict: false, throwOnError: false }],
	[rehypeHighlight, { detect: true, ignoreMissing: true }],
];

export function MarkdownBody({
	text,
	rawHtml = false,
	hardBreaks = false,
}: {
	text: string;
	rawHtml?: boolean;
	hardBreaks?: boolean;
}) {
	// rawHtml 时在 highlight 之前插入 rehype-raw：先把它内嵌的原始 HTML 解析成
	// hast 节点，再统一交给 highlight 做代码高亮，顺序不可颠倒。
	const rh: PluggableList = rawHtml ? [rehypeRaw, ...rehypePlugins] : rehypePlugins;
	return (
		<ReactMarkdown
			remarkPlugins={hardBreaks ? remarkPluginsHardBreaks : remarkPlugins}
			rehypePlugins={rh}
			components={{ pre: PlainCodeBlock, a: MdLink }}
		>
			{text}
		</ReactMarkdown>
	);
}

/** GFM markdown with syntax highlighting; code blocks get a copy button. */
export const Markdown = memo(function Markdown({ text, rawHtml = false, hardBreaks = false }: MarkdownProps) {
	return (
		<div className="md">
			<MarkdownBody text={text} rawHtml={rawHtml} hardBreaks={hardBreaks} />
		</div>
	);
});

/** 正文里的外链一律新窗口打开（`target=_blank` + `rel`）。 */
function MdLink({ href, children, ...rest }: JSX.IntrinsicElements["a"]) {
	const target = String(href ?? "");
	if (!/^(https?:|mailto:|tel:)/i.test(target)) {
		return (
			<a href={href} {...rest}>
				{children}
			</a>
		);
	}
	return (
		<a href={href} target="_blank" rel="noreferrer noopener" {...rest}>
			{children}
		</a>
	);
}

/** 普通代码块（高亮 + 行号 + 复制按钮）——默认展示。 */
function PlainCodeBlock({ children, ...props }: JSX.IntrinsicElements["pre"]) {
	// react-markdown 传进来的是 <pre><code …>…</code></pre> 里的 code 元素；
	// 按逻辑行切分的是它内部的 span/文本 children，而不是 code 元素本身
	// （否则每行会嵌套一个克隆的 <code>，且尾随空行无法被丢弃）。
	const inner =
		children && typeof children === "object" && "props" in children
			? (children as { props?: { children?: ReactNode } }).props?.children
			: children;
	const lines = splitCodeLines(inner);
	const multi = lines.length > 1;
	const numWidth = multi ? `${String(lines.length).length + 1}ch` : undefined;
	return (
		<div className="codeblock">
			<CopyButton text={codeText(children)} />
			<pre {...props}>
				{lines.map((nodes, i) => (
					<div className="code-line" key={i}>
						{multi && (
							<span className="code-num" style={numWidth ? { width: numWidth } : undefined}>
								{i + 1}
							</span>
						)}
						<code className="code-line-body hljs">{nodes}</code>
					</div>
				))}
			</pre>
		</div>
	);
}

function codeText(children: unknown): string {
	if (typeof children === "string") return children;
	if (Array.isArray(children)) return children.map(codeText).join("");
	if (children && typeof children === "object" && "props" in children) {
		const props = (children as { props?: { children?: unknown } }).props;
		return codeText(props?.children);
	}
	return "";
}
