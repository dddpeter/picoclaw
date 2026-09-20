// 移植自 pi-web-ui（MIT License）web/src/components/copy-button.tsx。
// SPDX-License-Identifier: MIT
// 适配点：i18n 由 pi-web-ui 的 useT() 换成 react-i18next 的 useTranslation()。
import { memo, useState } from "react";
import { FiCheck, FiCopy } from "react-icons/fi";
import { useTranslation } from "react-i18next";

export const CopyButton = memo(function CopyButton({ text }: { text: string }) {
	const { t } = useTranslation();
	const [copied, setCopied] = useState(false);
	if (!text) return null;
	return (
		<button
			type="button"
			className="copy-btn"
			title={t("piChat.copy")}
			onClick={() => {
				void navigator.clipboard.writeText(text).then(() => {
					setCopied(true);
					setTimeout(() => setCopied(false), 1200);
				});
			}}
		>
			{copied ? <FiCheck /> : <FiCopy />}
		</button>
	);
});
