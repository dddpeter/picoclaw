package agentplugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, path, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test skill\n---\n\n# " + name + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSkills(t *testing.T) {
	cases := []struct {
		name     string
		layout   func(t *testing.T, root string)
		want     []PluginSkill
		wantWarn bool
	}{
		{
			name: "two flat skills",
			layout: func(t *testing.T, root string) {
				writeSkill(t, filepath.Join(root, "skills", "a", "SKILL.md"), "a")
				writeSkill(t, filepath.Join(root, "skills", "b", "SKILL.md"), "b")
			},
			want: []PluginSkill{{Name: "a"}, {Name: "b"}},
		},
		{
			name: "nested SKILL.md not discovered",
			layout: func(t *testing.T, root string) {
				writeSkill(t, filepath.Join(root, "skills", "a", "SKILL.md"), "a")
				writeSkill(t, filepath.Join(root, "skills", "a", "nested", "SKILL.md"), "nested")
			},
			want: []PluginSkill{{Name: "a"}},
		},
		{
			name: "dir without SKILL.md skipped with warning",
			layout: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "skills", "empty"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeSkill(t, filepath.Join(root, "skills", "a", "SKILL.md"), "a")
			},
			want:     []PluginSkill{{Name: "a"}},
			wantWarn: true,
		},
		{
			name: "SKILL.md as directory skipped with warning",
			layout: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "skills", "a", "SKILL.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want:     nil,
			wantWarn: true,
		},
		{
			name:   "missing skills dir is silent",
			layout: func(t *testing.T, root string) {},
			want:   nil,
		},
		{
			name: "skills as file warns",
			layout: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "skills"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want:     nil,
			wantWarn: true,
		},
		{
			name: "frontmatter name mismatch with dir",
			layout: func(t *testing.T, root string) {
				writeSkill(t, filepath.Join(root, "skills", "b", "SKILL.md"), "a")
			},
			want:     nil,
			wantWarn: true,
		},
		{
			name: "frontmatter valid keeps dir name",
			layout: func(t *testing.T, root string) {
				writeSkill(t, filepath.Join(root, "skills", "deploy", "SKILL.md"), "deploy")
			},
			want: []PluginSkill{{Name: "deploy"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.layout(t, root)
			var r Report
			got := DiscoverSkills(root, &r)
			if len(got) != len(tc.want) {
				t.Fatalf("DiscoverSkills = %v (warnings: %v), want %v", got, r.Warnings, tc.want)
			}
			for i, w := range tc.want {
				if got[i].Name != w.Name {
					t.Errorf("skill[%d].Name = %q, want %q", i, got[i].Name, w.Name)
				}
				wantDir := filepath.Join(root, "skills", w.Name)
				if got[i].Dir != wantDir {
					t.Errorf("skill[%d].Dir = %q, want %q", i, got[i].Dir, wantDir)
				}
			}
			if tc.wantWarn && len(r.Warnings) == 0 {
				t.Errorf("expected warnings, got none")
			}
			if !tc.wantWarn && len(r.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", r.Warnings)
			}
		})
	}
}
