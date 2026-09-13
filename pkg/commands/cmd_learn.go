package commands

import (
	"fmt"
	"strings"
)

// learnCommand is metadata-only: behavior is intercepted in the agent loop
// (like /use) because it rewrites the pending user message into a full
// authoring turn instead of replying directly.
func learnCommand() Definition {
	return Definition{
		Name:        "learn",
		Description: "Turn a task, document, or what we just did into a reusable skill",
		Usage:       "/learn <topic | document | what we just did>",
	}
}

// learnPromptBudget caps pasted sources inside the learn prompt (bytes).
const learnPromptBudget = 24000

// BuildLearnPrompt assembles the one prompt that turns whatever the user
// described (a task we just did, a document, pasted notes) into a reusable
// skill. Adapted from hermes-agent's /learn prompt (docs/design/
// hermes-borrowing-analysis.zh.md §三) to picoclaw's tool and skill layout.
func BuildLearnPrompt(source string) string {
	source = strings.TrimSpace(source)
	if len(source) > learnPromptBudget {
		source = source[:learnPromptBudget] + "\n... (source truncated)"
	}

	var b strings.Builder
	b.WriteString(`Create a reusable skill from the source below. Work autonomously with your tools and report what you built at the end.

Process:
1. CHECK FIRST. Review the skill catalog in your context (or /list skills). If an existing skill covers the same territory, UPDATE that skill (read its SKILL.md, then patch) instead of creating a near-duplicate. Prefer class-level skills over hyper-specific ones — a name only meaningful for today's task is wrong.
2. GATHER. Read the source material with your tools. Prefer exact commands, paths, function signatures, and config keys that appear VERBATIM in the source. NEVER invent flags, paths, or APIs — if you didn't see it, don't write it.
3. WRITE the skill to <workspace>/skills/<skill-name>/SKILL.md via write_file, following the hard requirements below.
4. VERIFY. Re-read the file and confirm: valid frontmatter, every relative path referenced by the skill exists, and the Verification step actually proves the skill worked.

SKILL.md hard requirements:
- Frontmatter: ` + "`name`" + ` (lowercase-hyphenated, letters/digits/hyphens only), ` + "`description`" + ` (ONE sentence, capability-focused, no marketing words, under 1024 bytes). Optionally ` + "`disable-model-invocation: true`" + ` if the skill should only run when explicitly invoked.
- Body sections (omit a section only if it truly has no content): a 2-3 sentence intro (what it does, what it does NOT do); "## When to Use" (concrete trigger phrases); "## Prerequisites" (exact env vars, installs, credentials); "## How to Run"; "## Procedure" (numbered steps, copy-paste-exact commands); "## Pitfalls" (known limits, things that look broken but aren't); "## Verification" (one command/check that proves it worked).
- Frame actions through your tools: say ` + "`read_file`" + ` not cat, ` + "`write_file`" + ` not heredocs, ` + "`exec`" + ` for shell commands. Third-party CLIs are fine inside scripts.
- Keep it tight and scannable: ~100 lines for a simple skill, ~200 for a complex one. Do not re-paste the source. Larger scripts belong in scripts/ next to SKILL.md, referenced by relative path.
- Relative paths inside the skill resolve against the skill's own directory.

Finally, reply with: the skill name, one-line summary, whether you created it or updated an existing skill, and the verification result.

Source:
---
` + source + `
---
`)
	return b.String()
}

// ParseLearnSource extracts everything after "/learn".
func ParseLearnSource(input string) string {
	text := strings.TrimSpace(input)
	if text == "" {
		return ""
	}
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(text[len(parts[0]):])
}

// FormatLearnUsage is the reply for a bare /learn.
func FormatLearnUsage() string {
	return fmt.Sprintf("Usage: /learn <topic | document | what we just did>\nExample: /learn how we just deployed the gateway — write it up as a repeatable skill")
}
