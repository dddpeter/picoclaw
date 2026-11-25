package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	// defaultDenyPatterns is the fork's destructive-only guard: general
	// commands and scripts ($(), pipes, heredocs, sudo, git push, package
	// managers, …) are allowed by default; only data-destroying commands,
	// machine-stop commands, and writes into OS system directories are
	// refused. File tools get the equivalent protection via
	// fs.IsProtectedSystemPath; exec only blocks system-dir WRITES so
	// running/reading system paths stays possible.
	defaultDenyPatterns = []*regexp.Regexp{
		// Destructive deletions (rm needs BOTH r and f — combined in one flag
		// in any order, split across flags, or trailing the operands like
		// `rm file -rf`; plain `rm -r dir` stays allowed).
		regexp.MustCompile(`\brm\s+-[a-z]*r[a-z]*f\b`),
		regexp.MustCompile(`\brm\s+-[a-z]*f[a-z]*r\b`),
		regexp.MustCompile(
			`\brm\s+(?:-{1,2}[a-z-]+\s+)*(?:-{1,2}[a-z]*r[a-z]*\s+(?:-{1,2}[a-z-]+\s+)*-{1,2}[a-z]*f[a-z]*|-{1,2}[a-z]*f[a-z]*\s+(?:-{1,2}[a-z-]+\s+)*-{1,2}[a-z]*r[a-z]*)\b`,
		),
		regexp.MustCompile(`\brm\s+[^|;&<]*\s-[a-z]*r[a-z]*f\b`),
		regexp.MustCompile(`\brm\s+[^|;&<]*\s-[a-z]*f[a-z]*r\b`),
		regexp.MustCompile(
			`\brm\s+[^|;&<]*\s(?:-{1,2}[a-z-]+\s+)*(?:-{1,2}[a-z]*r[a-z]*\s+(?:-{1,2}[a-z-]+\s+)*-{1,2}[a-z]*f[a-z]*|-{1,2}[a-z]*f[a-z]*\s+(?:-{1,2}[a-z-]+\s+)*-{1,2}[a-z]*r[a-z]*)\b`,
		),
		regexp.MustCompile(`\brm\s+(-\S+\s+)*(--\S+\s+)*/\w*(\s|$|\*)`),
		regexp.MustCompile(`\brm\s+(-\S+\s+)*(--\S+\s+)*\.\s*$`),
		regexp.MustCompile(`\brm\s+(-\S+\s+)*(--\S+\s+)*\*\s*$`),
		regexp.MustCompile(`\bdel\s+/[fq]\b`),
		regexp.MustCompile(`\brd\s+/s\b`),
		regexp.MustCompile(`\brmdir\s+/s\b`),
		// NOTE: guardCommand lowercases the command before matching, so any
		// PowerShell cmdlet literals below must be written case-insensitively
		// ((?i:...)) to ever fire.
		regexp.MustCompile(`(?i:\bremove-item\s+[^|]*-recurse[^|]*-force)`),
		regexp.MustCompile(`(?i:\bremove-item\s+[^|]*-force[^|]*-recurse)`),
		// Disk wiping (must be followed by space/args).
		regexp.MustCompile(`(^|[^-\w])\b(format|mkfs|diskpart)\b\s`),
		regexp.MustCompile(`\bdd\s+if=`),
		// dd writing a BLOCK device regardless of operand order (octop §十一
		// audit: `dd of=/dev/sda` without a leading `if=` slipped past the
		// pattern above). The device list mirrors the `>` redirect rule below
		// on purpose: benign char devices (/dev/null, /dev/zero, /dev/random,
		// /dev/stdout) stay allowed — `dd of=/dev/null` is the canonical
		// throughput test and writing /dev/random is legitimate entropy
		// addition (issue #964 pinned the same line for redirects). Quoted
		// targets count; variable indirection (`of=$D`) is not statically
		// catchable and stays a documented gap like `rm -rf $DIR`.
		regexp.MustCompile(
			`\bdd\s+[^|;&]*\bof=("|')?/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d|dm-\d|md\d|sr\d|nbd\d|mapper/|disk/|block/)`,
		),
		// kill targeting every process (-1), PID 1, or the session's own
		// process group (0) — machine-killing (octop
		// dangerous_shell_commands.yaml CRITICAL); regular PIDs stay allowed.
		// The first rule is TARGET-SIDE (review P2-2): a benign-flagged kill
		// whose FIRST target is -1/-0/1/0 is fatal for the enumerated signal
		// spellings (`kill -15 -1`, `kill -- -1`, `kill -term 1`, bare
		// `kill 1`). Two
		// paths kill the ambiguity between "-1 as the SIGHUP SPELLING"
		// (kill -1 123 — benign, must stay allowed) and "-1 as the TARGET"
		// (kill -9 -1 — fatal): a bare -1/-0 target is only matched when a
		// signal flag or `--` precedes it (the flags chain eats the signal
		// position), while the zero-flag path only matches bare 1/0 — `kill -1`
		// alone is a usage error on every shell, so leaving it allowed is
		// correct. Signal flags are enumerated (numeric, common names,
		// `-s <name>`) so the introspection spelling `kill -l 1` (LIST signal
		// 1, kills nothing) and the existence probe `kill -0 <pid>` (signal 0)
		// never match. guardCommand lowercases, so -TERM arrives as -term.
		// The second rule covers the bare-word KILL spellings (-kill/-s kill/
		// -s9/-n 9).
		regexp.MustCompile(
			`\bkill\s+(?:(?:--\s+|-[1-9]\d*\s+|-s\s+(?:kill|term|hup|int|quit|usr1|usr2|[1-9]\d*)\s+|-(?:term|hup|int|quit|usr1|usr2|kill|tstp|cont|pipe|alrm)\s+|-sig(?:term|hup|int|quit|usr1|usr2|kill)\s+)+(-1\b|-0\b|1\b|0\b)|(?:1|0)\b)`,
		),
		regexp.MustCompile(
			`\bkill\s+(-\w+\s+)*(--\s+)?(-kill\b|-s\s*kill\b|-s\s*9\b|-n\s+9\b)\s+(-\w+\s+)*(--\s+)?(-1\b|1\b|0\b)`,
		),
		// The third rule is the TARGET-ANYWHERE fallback (review round 2
		// + 3): closes the shapes the flagged enumeration above structurally
		// misses — a benign pid ahead of the dangerous target (`kill 123
		// -1`, `kill -9 123 -1`), unenumerated signal names (`kill -s segv
		// -1`, `kill -s stop -1`), POSIX -n with a non-9 signum (`kill -n 15
		// -1`), and attached -s/-sig forms (`kill -sterm -1`). Instead of
		// enumerating signal spellings it enumerates ARGUMENTS as "any
		// token except exactly -l/-0" (RE2 has no lookahead), so the
		// introspection spellings can never match. Round 3 (2026-10-23
		// review): the old rule anchored the dangerous target to
		// END-OF-COMMAND — `kill 5 -1 123` (PID 1 / process group as a
		// later operand), `kill -s segv -1 789` and every quote/paren-wrapped
		// spelling (`bash -c 'kill 123 -1'`, `( kill 123 -1 )`, `kill 5 -1 #
		// comment`, trailing quote) slipped through, and a lone trailing
		// newline was a documented residual. The terminator class now
		// accepts ANY whitespace or shell delimiter (quotes, parens, comma,
		// backtick, bracket) so the target merely has to be a standalone
		// token, not the final one; plain \s in the class also covers the
		// newline case. Benign first-position bare-pid targets keep their
		// documented escape (`kill -1 123` signal-spelling vs `kill 1 123`
		// is not statically distinguishable — same residual class as
		// `rm -rf $DIR` variable indirection). NOTE: the backtick in the
		// terminator class is spliced via concatenation — a raw string
		// literal cannot contain one.
		regexp.MustCompile(
			`\bkill\s+(?:(?:[^\s;|&\r\n-][^\s;|&\r\n]*|-l[^\s;|&\r\n][^\s;|&\r\n]*|-0[^\s;|&\r\n][^\s;|&\r\n]*|-[^\s;|&\r\nl0][^\s;|&\r\n]*)\s+)+(?:-1\b|-0\b|1\b|0\b)(?:[\s;|&<>)'"` + "`" + `,\\\]]|$)`,
		),
		// Block writes to block devices (all common naming schemes).
		regexp.MustCompile(
			`>\s*/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d|dm-\d|md\d|sr\d|nbd\d)`,
		),
		regexp.MustCompile(`\bmkfs(\.\w+)?\s+/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d)`),
		regexp.MustCompile(`\bmv\b[^|;>]*\s/dev/null\b`),
		// 777 on root-ish paths; a leading sticky/setgid digit (1777 /tmp)
		// must NOT match, and leading 0 may.
		regexp.MustCompile(`\bchmod\s+(-[a-zA-Z]+\s+)*0?7\s*7\s*7\s+/\w*(\s|$|\*)`),
		// Machine stop.
		regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
		// Fork bomb.
		regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
		// Remote code execution via pipe-to-shell.
		regexp.MustCompile(`\bcurl\b.*\|\s*(?:sudo\s+)?(ba|z)?sh\b`),
		regexp.MustCompile(`\bwget\b.*\|\s*(?:sudo\s+)?(ba|z)?sh\b`),
		// Credential store overwrite.
		regexp.MustCompile(`>>?\s*~/?\.ssh/`),
		// Writes into Unix system directories — redirects, plus the common
		// write commands whose last argument is the destination (cp/mv/
		// install/tee). All three prefix lists below MUST stay aligned with
		// each other and with fs/system_paths.go's Unix prefixes.
		// Reading or executing system paths stays allowed; /var and /opt are
		// deliberately NOT listed (user-managed areas, matching the
		// file-tool protection list in fs/system_paths.go).
		regexp.MustCompile(
			`(^|[^|;&\w/])\s*>+\s*(/etc/|/boot/|/usr/|/bin/|/sbin/|/lib/|/lib64/|/lib32/|/libx32/|/run/|/proc/|/sys/)[^\s;|]*`,
		),
		regexp.MustCompile(
			`\b(cp|mv|install)\b[^|;>]*\s(/etc/|/boot/|/usr/|/bin/|/sbin/|/lib/|/lib64/|/lib32/|/libx32/|/run/|/proc/|/sys/)[^\s;|]*\s*$`,
		),
		regexp.MustCompile(
			`\btee\b\s+(-[a-z]+\s+)*(>>?)?\s*(/etc/|/boot/|/usr/|/bin/|/sbin/|/lib/|/lib64/|/lib32/|/libx32/|/run/|/proc/|/sys/)`,
		),
		// Windows system directory targeting (case-insensitive; redirects,
		// deletions, and copy/move with the system dir as destination).
		// progra~N covers the 8.3 short names of Program Files (x86/x64)
		// and ProgramData, whose ~N suffixes vary by install order.
		regexp.MustCompile(`>+\s*"?[A-Za-z]:\\(?i:windows)\b`),
		regexp.MustCompile(`>+\s*"?[A-Za-z]:\\(?i:program files|progra~[0-9])\b`),
		regexp.MustCompile(`>+\s*"?[A-Za-z]:\\(?i:programdata|progra~[0-9])\b`),
		regexp.MustCompile(`\b(del|erase|rd|(?i:remove-item)|rmdir)\s+[^|;]*[A-Za-z]:\\(?i:windows)\b`),
		regexp.MustCompile(
			`\b(del|erase|rd|(?i:remove-item)|rmdir)\s+[^|;]*[A-Za-z]:\\(?i:program files|progra~[0-9])\b`,
		),
		regexp.MustCompile(`\b((?i:copy-item)|(?i:move-item))\b[^|;]*[A-Za-z]:\\(?i:windows)\b`),
		regexp.MustCompile(`\b((?i:copy-item)|(?i:move-item))\b[^|;]*[A-Za-z]:\\(?i:program files|progra~[0-9])\b`),
		// Bash history substitution (e.g. ^rm^echo^ replays with edits).
		regexp.MustCompile(`^\s*\^[^\s|;&]+\^[^\s|;&]+`),
	}

	// strictDenyPatterns is the "strict" security-profile pattern set — the
	// upstream defaultDenyPatterns (as of upstream bbf6893c), preserved so
	// users can opt into upstream-equivalent strictness via
	// tools.exec.deny_profile: "strict". Beyond the fork's destructive-only
	// guard it also blocks everyday-but-powerful commands: sudo, command
	// substitution, pipes to shell, chmod/chown, kill, package managers,
	// docker, git push, ssh, eval, heredocs.
	strictDenyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
		regexp.MustCompile(`\bdel\s+/[fq]\b`),
		regexp.MustCompile(`\brmdir\s+/s\b`),
		// Match disk wiping commands (must be followed by space/args)
		regexp.MustCompile(`(^|[^-\w])\b(format|mkfs|diskpart)\b\s`),
		regexp.MustCompile(`\bdd\s+if=`),
		// Block writes to block devices (all common naming schemes).
		regexp.MustCompile(
			`>\s*/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d|dm-\d|md\d|sr\d|nbd\d)`,
		),
		regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
		regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
		regexp.MustCompile(`\$\([^)]+\)`),
		regexp.MustCompile(`\$\{[^}]+\}`),
		regexp.MustCompile("`[^`]+`"),
		regexp.MustCompile(`\|\s*sh\b`),
		regexp.MustCompile(`\|\s*bash\b`),
		regexp.MustCompile(`;\s*rm\s+-[rf]`),
		regexp.MustCompile(`&&\s*rm\s+-[rf]`),
		regexp.MustCompile(`\|\|\s*rm\s+-[rf]`),
		regexp.MustCompile(`(?i:<<\s*EOF)`),
		regexp.MustCompile(`\$\(\s*cat\s+`),
		regexp.MustCompile(`\$\(\s*curl\s+`),
		regexp.MustCompile(`\$\(\s*wget\s+`),
		regexp.MustCompile(`\$\(\s*which\s+`),
		regexp.MustCompile(`\bsudo\b`),
		regexp.MustCompile(`\bchmod\s+[0-7]{3,4}\b`),
		regexp.MustCompile(`\bchown\b`),
		regexp.MustCompile(`\bpkill\b`),
		regexp.MustCompile(`\bkillall\b`),
		regexp.MustCompile(`\bkill\b`),
		regexp.MustCompile(`\bcurl\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bwget\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bnpm\s+install\s+-g\b`),
		regexp.MustCompile(`\bpip\s+install\s+--user\b`),
		regexp.MustCompile(`\bapt\s+(install|remove|purge)\b`),
		regexp.MustCompile(`\byum\s+(install|remove)\b`),
		regexp.MustCompile(`\bdnf\s+(install|remove)\b`),
		regexp.MustCompile(`\bdocker\s+run\b`),
		regexp.MustCompile(`\bdocker\s+exec\b`),
		regexp.MustCompile(`\bgit\s+push\b`),
		regexp.MustCompile(`\bgit\s+reset\s+--hard\b`),
		regexp.MustCompile(`\bgit\s+clean\b[^|;&]*\s-[a-zA-Z]*f`),
		regexp.MustCompile(`\bssh\b.*@`),
		regexp.MustCompile(`\beval\b`),
		regexp.MustCompile(`\bsource\s+.*\.sh\b`),
	}

	// DenyProfileOpen is the fork's destructive-only guard ("open" profile).
	DenyProfileOpen = "open"
	// DenyProfileStrict is upstream-equivalent strictness.
	DenyProfileStrict = "strict"

	// windowsDenyPatterns was a PowerShell obfuscation guard (encoded
	// commands, [Text.Encoding] chains). Removed from the fork defaults: the
	// `-e` pattern false-positives on everyday flags (`pip install -e .`) and
	// the destructive-only default set above matches the fork's openness
	// policy. Revive from git history if the threat model changes.

	// absolutePathPattern matches absolute file paths in commands (Unix and Windows).
	// The drive-letter branch allows backslashes inside the class so
	// multi-component paths (C:\Users\me\file) match in full — stopping at
	// the first separator used to validate only "C:\Users" and misjudge
	// every nested path as outside the workspace.
	absolutePathPattern = regexp.MustCompile(`[A-Za-z]:\\[^\s\"']+|/[^\s\"']+`)

	// pathTraversalPattern blocks ../ chains in commands when the workspace
	// restriction is on. Package-level: guardCommand compiled it on every
	// call otherwise (review P2 - per-command hot path).
	pathTraversalPattern = regexp.MustCompile(`\.\.(?:[\\/]\.\.)*[\\/]`)

	// safePaths are kernel pseudo-devices that are always safe to reference in
	// commands, regardless of workspace restriction. They contain no user data
	// and cannot cause destructive writes.
	safePaths = map[string]bool{
		"/dev/null":    true,
		"/dev/zero":    true,
		"/dev/random":  true,
		"/dev/urandom": true,
		"/dev/stdin":   true,
		"/dev/stdout":  true,
		"/dev/stderr":  true,
	}
)

// execDeniedDeclinedSuffix mirrors the agent-side "Denied means declined"
// kernel rule (hookDeniedDeclinedSuffix): an exec deny hit is a policy
// decision, not a transient error - the model must not retry the same
// command verbatim or with cosmetic changes.
const execDeniedDeclinedSuffix = "The user's safety policy declined this action: do not retry it verbatim or with cosmetic changes; adjust your approach."

// expandPowerShellEnvVars expands environment variable syntax used by both
// PowerShell ($env:VAR) and CMD (%VAR%) to their actual values.
func expandPowerShellEnvVars(cmd string) string {
	// Handle PowerShell style: $env:VAR and ${env:VAR}
	rePs := regexp.MustCompile(`\$\{?env:(\w+)\}?`)
	cmd = rePs.ReplaceAllStringFunc(cmd, func(match string) string {
		varName := rePs.FindStringSubmatch(match)[1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match
	})

	// Handle CMD style: %VAR%
	reCmd := regexp.MustCompile(`%([^%]+)%`)
	return reCmd.ReplaceAllStringFunc(cmd, func(match string) string {
		varName := reCmd.FindStringSubmatch(match)[1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match
	})
}

func (t *ExecTool) commandMatchesAllowPattern(lower string) bool {
	for _, pattern := range t.allowPatterns {
		if pattern.MatchString(lower) {
			return true
		}
	}
	for _, pattern := range t.customAllowPatterns {
		if pattern.MatchString(lower) {
			return true
		}
	}
	return false
}

func (t *ExecTool) guardCommand(command, cwd string) string {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)

	// Deny patterns always apply, even when a command matches a custom allow rule.
	// Custom allow rules can permit a command, but must not disable secret-safety
	// deny rules such as jq env access checks (#3079).
	for _, pattern := range t.denyPatterns {
		if pattern.MatchString(lower) {
			// Declined-means-declined (agent-side kernel rule, mirrored here):
			// a deny hit is a policy decision, not a transient error - the
			// model must not retry the same command verbatim or with cosmetic
			// changes (review P1: the bare "dangerous pattern" text invited
			// retry-until-bypass).
			return "Command blocked by safety guard (dangerous pattern detected). " +
				execDeniedDeclinedSuffix
		}
	}

	// Allowlist mode activates only via SetAllowPatterns (library/embedder
	// API). custom_allow_patterns EXTENDS that list — per pinned semantics
	// (TestShellTool_CustomAllowDoesNotBecomeStrictAllowlist) it must never
	// become a standalone allowlist on its own.
	if len(t.allowPatterns) > 0 {
		if !t.commandMatchesAllowPattern(lower) {
			return "Command blocked by safety guard (not in allowlist)"
		}
	}

	if t.restrictToWorkspace {
		// Block path traversal patterns including .../.../ variants
		// (compiled once at package level - per-command hot path)
		if pathTraversalPattern.MatchString(cmd) {
			return "Command blocked by safety guard (path traversal detected)"
		}

		cwdPath, err := filepath.Abs(cwd)
		if err != nil {
			return ""
		}

		// Web URL schemes whose path components (starting with //) should be exempt
		// from workspace sandbox checks. file: is intentionally excluded so that
		// file:// URIs are still validated against the workspace boundary.
		webSchemes := []string{"http:", "https:", "ftp:", "ftps:", "sftp:", "ssh:", "git:"}

		// On Windows, expand ~ and PowerShell environment variables ($env:VAR) before path checking
		if runtime.GOOS == "windows" {
			// Expand PowerShell environment variables ($env:VAR and ${env:VAR})
			cmd = expandPowerShellEnvVars(cmd)
			// Also expand ~ for completeness
			if home, err := os.UserHomeDir(); err == nil {
				cmd = strings.ReplaceAll(cmd, "~", filepath.FromSlash(home))
			}
		}

		matchIndices := absolutePathPattern.FindAllStringIndex(cmd, -1)

		for _, loc := range matchIndices {
			raw := cmd[loc[0]:loc[1]]

			// Skip URL path components that look like they're from web URLs.
			// When a URL like "https://github.com" is parsed, the regex captures
			// "//github.com" as a match (the path portion after "https:").
			// Use the exact match position (loc[0]) so that duplicate //path substrings
			// in the same command are each evaluated at their own position.
			if strings.HasPrefix(raw, "//") && loc[0] > 0 {
				before := cmd[:loc[0]]
				isWebURL := false

				for _, scheme := range webSchemes {
					if strings.HasSuffix(before, scheme) {
						isWebURL = true
						break
					}
				}

				if isWebURL {
					continue
				}
			}

			// Skip scheme-less URL paths like "wttr.in/Beijing".
			// When a /path is immediately preceded by a token that looks
			// like a domain name and that token does NOT exist as a local
			// filesystem entry, treat the path as part of a URL and skip
			// workspace sandbox validation.
			//
			// The local-path-exists guard prevents symlink bypass: if
			// "foo.bar" exists as a local symlink or directory, the path
			// still undergoes full workspace validation (see #2965).
			if loc[0] > 0 && raw[0] == '/' {
				// Find the token immediately before the "/".
				j := loc[0] - 1
				for j >= 0 && !isShellTokenBoundary(cmd[j]) {
					j--
				}
				token := cmd[j+1 : loc[0]]
				if looksLikeDomain(token) && !localPathExists(cwd, token) {
					continue
				}
			}

			pText := commandPathTextFromMatch(cmd, loc[0], loc[1])

			// file:// URIs are validated as plain filesystem paths (issue
			// #1254): "file:///etc/passwd" → "/etc/passwd", Windows-style
			// "file://C:\x" → "C:\x". "file:" is deliberately absent from the
			// web-scheme exemptions above, so the match starts right after the
			// scheme — drop the "//" authority marker before path checks.
			if loc[0] >= 5 && cmd[loc[0]-5:loc[0]] == "file:" && strings.HasPrefix(pText, "//") {
				pText = pText[2:]
			}

			// safePaths are keyed by their canonical POSIX spelling; match on
			// the command text because Windows re-rooting below maps
			// /dev/null to <drive>:\dev\null, which would defeat the exemption.
			if safePaths[pText] {
				continue
			}

			p, err := commandPathAbs(pText, cwdPath)
			if err != nil {
				continue
			}

			// Windows-specific: normalize paths to block ADS and extended-length paths
			if runtime.GOOS == "windows" {
				// Strip \\?\ prefix (extended-length path)
				p = strings.TrimPrefix(p, `\\?\`)
				// Strip NTFS alternate data streams (only if colon is not at position 1 = drive letter)
				if idx := strings.Index(p, ":"); idx > 1 {
					p = p[:idx]
				}
			}

			// Check symlinks and junctions
			resolved, err := filepath.EvalSymlinks(p)
			if err == nil {
				p = resolved
			}

			if safePaths[p] {
				continue
			}
			if isAllowedPath(p, t.allowedPathPatterns) {
				continue
			}

			rel, err := filepath.Rel(cwdPath, p)
			if err != nil {
				// On Windows Rel fails when the target sits on another drive
				// or a UNC share (//host/share). Those can never be inside
				// the working dir — fail closed instead of skipping.
				return "Command blocked by safety guard (path outside working dir)"
			}

			if strings.HasPrefix(rel, "..") {
				return "Command blocked by safety guard (path outside working dir)"
			}
		}
	}

	return ""
}

func commandPathAbs(pathText, cwdPath string) (string, error) {
	if runtime.GOOS == "windows" {
		// PowerShell normalizes forward slashes and resolves rooted paths
		// ("/tmp", "\tmp") against the drive of the current directory,
		// while filepath classifies them as relative — validate the path
		// the shell will actually resolve, not the filepath fiction.
		pathText = filepath.FromSlash(pathText)
		if len(pathText) > 0 && (pathText[0] == '/' || pathText[0] == '\\') {
			if vol := filepath.VolumeName(cwdPath); vol != "" {
				pathText = vol + pathText
			}
		}
	}
	if filepath.IsAbs(pathText) {
		return filepath.Abs(pathText)
	}
	return filepath.Abs(filepath.Join(cwdPath, pathText))
}

func commandPathTextFromMatch(cmd string, start, end int) string {
	raw := cmd[start:end]
	if !strings.HasPrefix(raw, "/") || isUnixAbsolutePathMatchStart(cmd, start) {
		return raw
	}

	tokenStart, tokenEnd := shellTokenBounds(cmd, start)
	prefix := cmd[tokenStart:start]
	// For --flag=rel/path, validate the value. For ambiguous attached option
	// forms like -isystem/path, keep the slash-starting path conservative.
	if eq := strings.IndexByte(prefix, '='); eq >= 0 {
		return cmd[tokenStart+eq+1 : tokenEnd]
	}
	if strings.HasPrefix(prefix, "-") {
		return raw
	}
	return cmd[tokenStart:tokenEnd]
}

func shellTokenBounds(cmd string, idx int) (int, int) {
	start := idx
	for start > 0 && !isShellTokenBoundary(cmd[start-1]) {
		start--
	}
	end := idx
	for end < len(cmd) && !isShellTokenBoundary(cmd[end]) {
		end++
	}
	return start, end
}

// isUnixAbsolutePathMatchStart returns true when a regex match beginning with
// "/" is actually an absolute path token, not the separator inside a relative
// path such as "skills/foo.py".
func isUnixAbsolutePathMatchStart(cmd string, idx int) bool {
	if idx <= 0 {
		return true
	}

	prev := cmd[idx-1]
	if isShellTokenBoundary(prev) || prev == '=' || prev == ',' || prev == '(' || prev == '[' || prev == '{' {
		return true
	}

	j := idx - 1
	for j >= 0 && !isShellTokenBoundary(cmd[j]) {
		j--
	}
	prefix := cmd[j+1 : idx]

	return strings.HasPrefix(prefix, "-") && !strings.Contains(prefix, "=")
}

// isShellTokenBoundary returns true when b is a byte that separates
// tokens in a shell command (space, tab, colon, semicolon, pipe, etc.).
func isShellTokenBoundary(b byte) bool {
	switch b {
	case ' ', '\t', ':', ';', '|', '&', '<', '>', '\'', '"', '`', '\n', '\r':
		return true
	}
	return false
}

// looksLikeDomain returns true when s looks like a DNS domain name:
// it contains at least one dot, starts with an alphanumeric character,
// and does not end with a common file extension.
func looksLikeDomain(s string) bool {
	if len(s) < 3 || !strings.ContainsRune(s, '.') {
		return false
	}
	first := s[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	// Exclude tokens ending with common file/programming extensions,
	// e.g. "script.py", "main.go", "app.exe".
	if idx := strings.LastIndexByte(s, '.'); idx >= 0 {
		ext := strings.ToLower(s[idx+1:])
		if commonFileExtension(ext) {
			return false
		}
	}
	return true
}

// commonFileExtension returns true when ext is a file extension that
// strongly indicates a local file rather than a domain TLD.
func commonFileExtension(ext string) bool {
	switch ext {
	case "py", "js", "ts", "tsx", "jsx", "go", "rs", "rb", "php",
		"java", "c", "cpp", "h", "hpp", "cs", "swift", "kt", "scala",
		"sh", "bash", "zsh", "fish", "ps1", "bat", "cmd",
		"txt", "md", "rst", "log", "json", "yaml", "yml", "toml",
		"xml", "html", "css", "scss", "ini", "cfg", "conf", "env",
		"exe", "dll", "so", "dylib", "lib", "a", "o", "obj",
		"zip", "tar", "gz", "bz2", "xz", "7z", "rar",
		"png", "jpg", "jpeg", "gif", "svg", "ico", "bmp", "webp",
		"mp3", "mp4", "wav", "avi", "mov", "mkv", "flac",
		"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
		"pub", "pem", "key", "crt", "cer", "p12", "pfx",
		"bak", "tmp", "swp", "lock",
		"ttf", "otf", "woff", "woff2", "eot",
		"deb", "rpm", "apk", "msi", "dmg",
		"sql", "sqlite", "db":
		return true
	}
	return false
}

// localPathExists returns true when the given token resolves to an
// existing filesystem entry relative to cwd.
func localPathExists(cwd, token string) bool {
	info, err := os.Lstat(filepath.Join(cwd, token))
	return err == nil && info != nil
}
