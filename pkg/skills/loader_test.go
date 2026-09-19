package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkillsInfoValidate(t *testing.T) {
	testcases := []struct {
		name        string
		skillName   string
		description string
		wantErr     bool
		errContains []string
	}{
		{
			name:        "valid-skill",
			skillName:   "valid-skill",
			description: "a valid skill description",
			wantErr:     false,
		},
		{
			name:        "empty-name",
			skillName:   "",
			description: "description without name",
			wantErr:     true,
			errContains: []string{"name is required"},
		},
		{
			name:        "empty-description",
			skillName:   "skill-without-description",
			description: "",
			wantErr:     true,
			errContains: []string{"description is required"},
		},
		{
			name:        "empty-both",
			skillName:   "",
			description: "",
			wantErr:     true,
			errContains: []string{"name is required", "description is required"},
		},
		{
			name:        "name-with-spaces",
			skillName:   "skill with spaces",
			description: "spaces are allowed in skill names",
			wantErr:     false,
		},
		{
			name:        "name-with-underscore",
			skillName:   "skill_underscore",
			description: "underscores are allowed in skill names",
			wantErr:     false,
		},
		{
			name:        "name-with-cjk",
			skillName:   "SVG绘图工作台-智能生图",
			description: "unicode letters are allowed",
			wantErr:     false,
		},
		{
			name:        "name-with-parens",
			skillName:   "Proactivity (Proactive Agent)",
			description: "parens and spaces are allowed",
			wantErr:     false,
		},
		{
			name:        "name-with-path-separator",
			skillName:   "skill/sub",
			description: "path separators are invalid",
			wantErr:     true,
			errContains: []string{"skill name is invalid"},
		},
		{
			name:        "name-with-backslash",
			skillName:   `skill\sub`,
			description: "backslashes are invalid",
			wantErr:     true,
			errContains: []string{"skill name is invalid"},
		},
		{
			name:        "name-with-dotdot",
			skillName:   "skill..bad",
			description: "traversal sequences are invalid",
			wantErr:     true,
			errContains: []string{"skill name is invalid"},
		},
		{
			name:        "name-with-question-mark",
			skillName:   "bad?name",
			description: "windows-forbidden runes are invalid",
			wantErr:     true,
			errContains: []string{`must not contain "?"`},
		},
		{
			name:        "name-windows-reserved",
			skillName:   "con",
			description: "windows device names are invalid",
			wantErr:     true,
			errContains: []string{"reserved device name"},
		},
		{
			name:        "name-leading-dot",
			skillName:   ".hidden",
			description: "names must not start with a dot",
			wantErr:     true,
			errContains: []string{"must not start with a dot"},
		},
		{
			name:        "name-trailing-dot",
			skillName:   "bad.",
			description: "names must not end with a dot",
			wantErr:     true,
			errContains: []string{"must not end with a dot or space"},
		},
		{
			name:        "name-with-control-char",
			skillName:   "bad\tname",
			description: "control characters are invalid",
			wantErr:     true,
			errContains: []string{"control characters"},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			info := SkillInfo{
				Name:        tc.skillName,
				Description: tc.description,
			}
			err := info.validate()
			if tc.wantErr {
				assert.Error(t, err)
				for _, msg := range tc.errContains {
					assert.ErrorContains(t, err, msg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExtractFrontmatter(t *testing.T) {
	sl := &SkillsLoader{}

	testcases := []struct {
		name           string
		content        string
		expectedName   string
		expectedDesc   string
		lineEndingType string
	}{
		{
			name:           "unix-line-endings",
			lineEndingType: "Unix (\\n)",
			content:        "---\nname: test-skill\ndescription: A test skill\n---\n\n# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
		{
			name:           "windows-line-endings",
			lineEndingType: "Windows (\\r\\n)",
			content:        "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n\r\n# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
		{
			name:           "classic-mac-line-endings",
			lineEndingType: "Classic Mac (\\r)",
			content:        "---\rname: test-skill\rdescription: A test skill\r---\r\r# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			// Extract frontmatter
			frontmatter := sl.extractFrontmatter(tc.content)
			assert.NotEmpty(t, frontmatter, "Frontmatter should be extracted for %s line endings", tc.lineEndingType)

			// Parse YAML to get name and description (parseSimpleYAML now handles all line ending types)
			yamlMeta := sl.parseSimpleYAML(frontmatter)
			assert.Equal(
				t,
				tc.expectedName,
				yamlMeta["name"],
				"Name should be correctly parsed from frontmatter with %s line endings",
				tc.lineEndingType,
			)
			assert.Equal(
				t,
				tc.expectedDesc,
				yamlMeta["description"],
				"Description should be correctly parsed from frontmatter with %s line endings",
				tc.lineEndingType,
			)
		})
	}
}

// createSkillDir creates a skill directory with a SKILL.md file containing the given frontmatter.
func createSkillDir(t *testing.T, base, dirName, name, description string) {
	t.Helper()
	dir := filepath.Join(base, dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func TestListSkillsWorkspaceOverridesGlobal(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	createSkillDir(t, filepath.Join(ws, "skills"), "my-skill", "my-skill", "workspace version")
	createSkillDir(t, global, "my-skill", "my-skill", "global version")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "workspace", skills[0].Source)
	assert.Equal(t, "workspace version", skills[0].Description)
}

func TestListSkillsGlobalOverridesBuiltin(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")

	createSkillDir(t, global, "my-skill", "my-skill", "global version")
	createSkillDir(t, builtin, "my-skill", "my-skill", "builtin version")

	sl := NewSkillsLoader(ws, global, builtin)
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "global", skills[0].Source)
	assert.Equal(t, "global version", skills[0].Description)
}

func TestListSkillsMetadataNameDedup(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	// Different directory names but same metadata name
	createSkillDir(t, filepath.Join(ws, "skills"), "dir-a", "shared-name", "workspace version")
	createSkillDir(t, global, "dir-b", "shared-name", "global version")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "shared-name", skills[0].Name)
	assert.Equal(t, "workspace", skills[0].Source)
}

func TestListSkillsMultipleDistinctSkills(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")

	createSkillDir(t, filepath.Join(ws, "skills"), "skill-a", "skill-a", "desc a")
	createSkillDir(t, global, "skill-b", "skill-b", "desc b")
	createSkillDir(t, builtin, "skill-c", "skill-c", "desc c")

	sl := NewSkillsLoader(ws, global, builtin)
	skills := sl.ListSkills()

	assert.Len(t, skills, 3)
	names := map[string]string{}
	for _, s := range skills {
		names[s.Name] = s.Source
	}
	assert.Equal(t, "workspace", names["skill-a"])
	assert.Equal(t, "global", names["skill-b"])
	assert.Equal(t, "builtin", names["skill-c"])
}

func TestListSkillsInvalidSkillSkipped(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	// Invalid name (path separator is always rejected)
	bad := filepath.Join(ws, "skills", "bad_name")
	require.NoError(t, os.MkdirAll(bad, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte(
		"---\nname: bad/name\ndescription: desc\n---\n\n# bad"), 0o644))
	// Valid skill
	createSkillDir(t, global, "good-skill", "good-skill", "desc")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	// 2026-09-19 semantics change: an invalid declared name no longer drops
	// the skill — it falls back to the directory basename (special-character
	// support), so the workspace skill survives under "bad_name".
	require.Len(t, skills, 2)
	names := []string{skills[0].Name, skills[1].Name}
	assert.Contains(t, names, "good-skill")
	assert.Contains(t, names, "bad_name")
}

func TestListSkillsNestedDirs(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")

	// Namespace-style nesting: skills/@ns/slug/SKILL.md
	createSkillDir(t, filepath.Join(ws, "skills", "@ns"), "slug", "nested-skill", "nested skill")
	// Deeper nesting without SKILL.md in intermediate dirs
	createSkillDir(t, filepath.Join(ws, "skills", "group", "vendor"), "deep-skill", "deep-skill", "deep skill")
	// Flat skill still works
	createSkillDir(t, filepath.Join(ws, "skills"), "flat-skill", "flat-skill", "flat skill")

	sl := NewSkillsLoader(ws, "", "")
	skills := sl.ListSkills()
	require.Len(t, skills, 3)

	byName := map[string]SkillInfo{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	assert.Contains(t, byName, "nested-skill")
	assert.Contains(t, byName, "deep-skill")
	assert.Contains(t, byName, "flat-skill")

	content, ok := sl.LoadSkill("nested-skill")
	require.True(t, ok, "nested skill must load even though dir basename != name")
	assert.Contains(t, content, "# nested-skill")

	// No SKILL.md in intermediate directories must not swallow nested skills.
	hiddenRoot := filepath.Join(ws, "skills", ".hidden")
	createSkillDir(t, hiddenRoot, "inside-hidden", "inside-hidden", "hidden desc")
	sl2 := NewSkillsLoader(ws, "", "")
	assert.NotContains(t, func() []string {
		var names []string
		for _, s := range sl2.ListSkills() {
			names = append(names, s.Name)
		}
		return names
	}(), "inside-hidden", "skills under hidden dirs must be skipped")
}

func TestListSkillsEmptyAndNonexistentDirs(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	emptyDir := filepath.Join(tmp, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	sl := NewSkillsLoader(ws, emptyDir, filepath.Join(tmp, "nonexistent"))
	skills := sl.ListSkills()

	assert.Empty(t, skills)
}

func TestListSkillsDirWithoutSkillMD(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	// Directory exists but has no SKILL.md
	require.NoError(t, os.MkdirAll(filepath.Join(global, "no-skillmd"), 0o755))
	// Valid skill alongside
	createSkillDir(t, global, "real-skill", "real-skill", "desc")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "real-skill", skills[0].Name)
}

func TestStripFrontmatter(t *testing.T) {
	sl := &SkillsLoader{}

	testcases := []struct {
		name            string
		content         string
		expectedContent string
		lineEndingType  string
	}{
		{
			name:            "unix-line-endings",
			lineEndingType:  "Unix (\\n)",
			content:         "---\nname: test-skill\ndescription: A test skill\n---\n\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "windows-line-endings",
			lineEndingType:  "Windows (\\r\\n)",
			content:         "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n\r\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "classic-mac-line-endings",
			lineEndingType:  "Classic Mac (\\r)",
			content:         "---\rname: test-skill\rdescription: A test skill\r---\r\r# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "unix-line-endings-without-trailing-newline",
			lineEndingType:  "Unix (\\n) without trailing newline",
			content:         "---\nname: test-skill\ndescription: A test skill\n---\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "windows-line-endings-without-trailing-newline",
			lineEndingType:  "Windows (\\r\\n) without trailing newline",
			content:         "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "no-frontmatter",
			lineEndingType:  "No frontmatter",
			content:         "# Skill Content\n\nSome content here.",
			expectedContent: "# Skill Content\n\nSome content here.",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			result := sl.stripFrontmatter(tc.content)
			assert.Equal(
				t,
				tc.expectedContent,
				result,
				"Frontmatter should be stripped correctly for %s",
				tc.lineEndingType,
			)
		})
	}
}

func TestSkillRootsTrimsWhitespaceAndDedups(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")
	home := filepath.Join(tmp, "home")

	roots := ResolveSkillRoots(workspace, "  "+global+"  ", "\t"+builtin+"\n", home)

	assert.Equal(t, []SkillRoot{
		{Dir: filepath.Join(workspace, "skills"), Source: SourceWorkspace},
		{Dir: filepath.Join(workspace, ".skills"), Source: SourceProject},
		{Dir: global, Source: SourceGlobal},
		{Dir: filepath.Join(home, ".agents", "skills"), Source: SourceGlobal},
		{Dir: builtin, Source: SourceBuiltin},
	}, roots)

	// Duplicate directories collapse to their first (highest priority) entry;
	// an unknown home drops only the ~/.agents root.
	dup := ResolveSkillRoots(workspace, filepath.Join(workspace, "skills"), builtin, "")
	assert.Equal(t, []SkillRoot{
		{Dir: filepath.Join(workspace, "skills"), Source: SourceWorkspace},
		{Dir: filepath.Join(workspace, ".skills"), Source: SourceProject},
		{Dir: builtin, Source: SourceBuiltin},
	}, dup)

	sl := NewSkillsLoaderFromRoots(workspace, roots)
	assert.Equal(t, []string{
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, ".skills"),
		global,
		filepath.Join(home, ".agents", "skills"),
		builtin,
	}, sl.SkillRoots())
}

func TestListSkillsProjectDotSkillsDir(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	createSkillDir(t, filepath.Join(ws, ".skills"), "project-only", "project-only", "from .skills")
	createSkillDir(t, filepath.Join(ws, ".skills"), "shared", "shared", ".skills version")
	createSkillDir(t, filepath.Join(ws, "skills"), "shared", "shared", "skills version")
	createSkillDir(t, global, "shared", "shared", "global version")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()
	require.Len(t, skills, 2)

	byName := map[string]SkillInfo{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	assert.Equal(t, SourceProject, byName["project-only"].Source)
	assert.Equal(t, "from .skills", byName["project-only"].Description)
	// <workspace>/skills outranks <workspace>/.skills, which outranks global.
	assert.Equal(t, SourceWorkspace, byName["shared"].Source)
	assert.Equal(t, "skills version", byName["shared"].Description)

	content, ok := sl.LoadSkill("project-only")
	require.True(t, ok)
	assert.Contains(t, content, "# project-only")
}

func TestListSkillsHomeDotAgentsDir(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	createSkillDir(t, filepath.Join(home, ".agents", "skills"), "cross-tool", "cross-tool", "shared with other agents")

	sl := NewSkillsLoader(ws, filepath.Join(tmp, "global"), "")
	skills := sl.ListSkills()
	require.Len(t, skills, 1)
	assert.Equal(t, "cross-tool", skills[0].Name)
	assert.Equal(t, SourceGlobal, skills[0].Source)
	assert.Equal(t, filepath.Join(home, ".agents", "skills", "cross-tool", "SKILL.md"), skills[0].Path)
}

func TestListSkillsClampsLongDescription(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	long := strings.Repeat("描述", MaxDescriptionLength) // far over the byte cap

	createSkillDir(t, filepath.Join(ws, "skills"), "wordy", "wordy", long)

	sl := NewSkillsLoader(ws, "", "")
	skills := sl.ListSkills()
	require.Len(t, skills, 1, "an over-long description must clamp, not drop the skill")
	desc := skills[0].Description
	assert.LessOrEqual(t, len(desc), MaxDescriptionLength)
	assert.True(t, utf8.ValidString(desc), "clamp must not split a rune")
	assert.True(t, strings.HasSuffix(desc, "…"))
}

func TestDisableModelInvocationHidesFromCatalog(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")

	plain := filepath.Join(ws, "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plain, "SKILL.md"), []byte(
		"---\nname: plain-skill\ndescription: visible in catalog\n---\n\n# plain"), 0o644))

	hidden := filepath.Join(ws, "skills", "manual-only")
	require.NoError(t, os.MkdirAll(hidden, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte(
		"---\nname: manual-only\ndescription: explicit invocation only\ndisable-model-invocation: true\n---\n\n# manual"), 0o644))

	sl := NewSkillsLoader(ws, "", "")
	skills := sl.ListSkills()
	require.Len(t, skills, 2, "the flagged skill must still load (explicit /use keeps working)")
	for _, s := range skills {
		assert.Equal(t, s.Name == "manual-only", s.DisableModelInvocation, "flag must parse for %s", s.Name)
	}

	summary := sl.BuildSkillsSummary()
	assert.Contains(t, summary, "plain-skill")
	assert.NotContains(t, summary, "manual-only", "disable-model-invocation must hide the skill from the model catalog")
}

func TestGetSkillMetadata_UsesMarkdownParagraphWhenNoFrontmatter(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "# Plain Skill\n\nThis is parsed from markdown paragraph.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	// The H1 is a valid skill name now, so it wins over the directory name.
	assert.Equal(t, "Plain Skill", meta.Name)
	assert.Equal(t, "This is parsed from markdown paragraph.", meta.Description)
}

func TestGetSkillMetadata_FrontmatterOverridesMarkdown(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "---\nname: frontmatter-skill\ndescription: frontmatter description\n---\n\n# Plain Skill\n\nBody description.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "frontmatter-skill", meta.Name)
	assert.Equal(t, "frontmatter description", meta.Description)
}

func TestGetSkillMetadata_YAMLMultilineDescription(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "---\nname: frontmatter-skill\ndescription: |\n  line 1: with colon\n  line 2\n---\n\n# Plain Skill\n\nBody description.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "frontmatter-skill", meta.Name)
	assert.Equal(t, "line 1: with colon\nline 2", meta.Description)
}

func TestGetSkillMetadata_InvalidHeadingNameFallsBackToDirName(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "valid-name")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "# Invalid?Heading Name\n\nBody description.\n" // "?" is a Windows-forbidden rune
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "valid-name", meta.Name)
	assert.Equal(t, "Body description.", meta.Description)
}

func TestGetSkillMetadata_IgnoresHTMLCommentBlocks(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "biomed-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "<!--\n# COPYRIGHT NOTICE\n# This file is part of the \"Universal Biomedical Skills\" project.\n# Copyright (c) 2026 MD BABU MIA, PhD <md.babu.mia@mssm.edu>\n# All Rights Reserved.\n#\n# This code is proprietary and confidential.\n# Unauthorized copying of this file, via any medium is strictly prohibited.\n#\n# Provenance: Authenticated by MD BABU MIA\n\n-->\n\n# Biomed Skill\n\nSummarize biomedical papers.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	// The HTML comment is ignored; the real H1 "Biomed Skill" is a valid
	// name now (spaces allowed), so it wins over the directory basename.
	assert.Equal(t, "Biomed Skill", meta.Name)
	assert.Equal(t, "Summarize biomedical papers.", meta.Description)
}

// TestListSkills_DeepNestingAndSymlinkedDirs verifies discovery beyond the
// old depth-4 limit and through directory symlinks (npm-style installs).
func TestListSkills_DeepNestingAndSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	// 6 levels deep — old limit was 4.
	deep := filepath.Join(root, "l1", "l2", "l3", "l4", "deep-skill")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "SKILL.md"), []byte("---\nname: deep-skill\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlinked skill dir (skip when the platform/privilege denies it).
	linkTarget := filepath.Join(root, "real-skill")
	if err := os.MkdirAll(linkTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkTarget, "SKILL.md"), []byte("---\nname: linked-skill\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-skill")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Logf("symlink unavailable on this host (%v); skipping symlink part", err)
	} else {
		t.Cleanup(func() { _ = os.Remove(link) })
	}

	sl := NewSkillsLoaderFromRoots(root, []SkillRoot{{Dir: root, Source: "workspace"}})
	found := map[string]bool{}
	for _, s := range sl.ListSkills() {
		found[s.Name] = true
	}
	if !found["deep-skill"] {
		t.Fatalf("deep-nested skill not discovered: %v", found)
	}
	if _, statErr := os.Lstat(link); statErr == nil && !found["linked-skill"] {
		t.Fatalf("symlinked skill dir not discovered: %v", found)
	}
}

// TestListSkills_SpecialCharFrontmatterNameFallsBack pins that a frontmatter
// name failing validation (exotic runes) no longer drops the skill: the
// directory basename is used instead.
func TestListSkills_SpecialCharFrontmatterNameFallsBack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ok-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "what? really" contains a Windows-forbidden rune → validation fails.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: what? really\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	sl := NewSkillsLoaderFromRoots(root, []SkillRoot{{Dir: root, Source: "workspace"}})
	skills := sl.ListSkills()
	if len(skills) != 1 {
		t.Fatalf("skills = %d, want 1 (fallback keeps the skill)", len(skills))
	}
	if skills[0].Name != "ok-dir" {
		t.Fatalf("fallback name = %q, want directory basename", skills[0].Name)
	}
	if _, ok := sl.LoadSkill("ok-dir"); !ok {
		t.Fatal("fallback skill must be loadable")
	}
}
