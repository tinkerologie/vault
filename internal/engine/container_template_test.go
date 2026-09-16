package engine

import (
	"strings"
	"testing"
)

// TestRewriteTemplateVolumePaths is the regression test for the second half of
// issue #336: a custom-destination restore rewrote the container's binds but
// copied the Unraid template back untouched, so the Docker page's "Volume
// Mappings" column and its Edit form disagreed — and saving Edit silently
// reverted the remap.
func TestRewriteTemplateVolumePaths(t *testing.T) {
	t.Parallel()

	const modern = `<Container>
  <Config Name="Appdata" Target="/config" Default="/mnt/user/appdata/plex" Mode="rw" Type="Path">/mnt/user/appdata/plex</Config>
  <Config Name="Media" Target="/media" Default="/mnt/user/media" Mode="ro" Type="Path">/mnt/user/media</Config>
</Container>`

	cases := []struct {
		name       string
		template   string
		rewrites   []volumePathRewrite
		wantHas    []string
		wantHasNot []string
	}{
		{
			name:     "rewrites both the element text and the Default attribute",
			template: modern,
			rewrites: []volumePathRewrite{{Old: "/mnt/user/appdata/plex", New: "/mnt/user/restore/plex"}},
			wantHas: []string{
				`Default="/mnt/user/restore/plex"`,
				`Type="Path">/mnt/user/restore/plex</Config>`,
			},
			wantHasNot: []string{"/mnt/user/appdata/plex"},
		},
		{
			name:     "no rewrites leaves the template byte-for-byte",
			template: modern,
			rewrites: nil,
			wantHas:  []string{modern},
		},
		{
			name:     "a path with no remapped mount is untouched",
			template: modern,
			rewrites: []volumePathRewrite{{Old: "/mnt/user/appdata/plex", New: "/mnt/user/restore/plex"}},
			wantHas:  []string{`Default="/mnt/user/media"`, `>/mnt/user/media</Config>`},
		},
		{
			name:     "a longer sibling path is not rewritten",
			template: `<Config Type="Path">/mnt/user/appdata/plex-config</Config>`,
			rewrites: []volumePathRewrite{{Old: "/mnt/user/appdata/plex", New: "/mnt/user/restore/plex"}},
			wantHas:  []string{"/mnt/user/appdata/plex-config"},
		},
		{
			name:     "a subdirectory of a remapped mount follows its parent",
			template: `<Config Type="Path">/mnt/user/appdata/plex/Library</Config>`,
			rewrites: []volumePathRewrite{{Old: "/mnt/user/appdata/plex", New: "/mnt/user/restore/plex"}},
			wantHas:  []string{"/mnt/user/restore/plex/Library"},
		},
		{
			name:     "the most specific of two nested mounts wins",
			template: `<Config Type="Path">/mnt/user/appdata/plex/db</Config><Config Type="Path">/mnt/user/appdata</Config>`,
			rewrites: []volumePathRewrite{
				{Old: "/mnt/user/appdata", New: "/mnt/restore/appdata"},
				{Old: "/mnt/user/appdata/plex", New: "/mnt/restore/plex"},
			},
			wantHas: []string{"/mnt/restore/plex/db", "/mnt/restore/appdata"},
		},
		{
			name:     "a no-op rewrite is skipped",
			template: `<Config Type="Path">/mnt/user/appdata</Config>`,
			rewrites: []volumePathRewrite{{Old: "/mnt/user/appdata", New: "/mnt/user/appdata"}},
			wantHas:  []string{"/mnt/user/appdata"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := string(rewriteTemplateVolumePaths([]byte(tc.template), tc.rewrites))
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("template is missing %q:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.wantHasNot {
				if strings.Contains(got, unwanted) {
					t.Errorf("template still contains %q:\n%s", unwanted, got)
				}
			}
		})
	}

	t.Run("a substitution is never rewritten again", func(t *testing.T) {
		t.Parallel()

		// The restore destination normally lives under a share the container
		// also binds, so a shorter rewrite applied afterwards would re-match
		// inside the text the longer one just produced.
		got := string(rewriteTemplateVolumePaths(
			[]byte(`<Config Default="/mnt/user/appdata/plex">/mnt/user</Config>`),
			[]volumePathRewrite{
				{Old: "/mnt/user", New: "/mnt/user/restore/user"},
				{Old: "/mnt/user/appdata/plex", New: "/mnt/user/restore/plex"},
			}))
		want := `<Config Default="/mnt/user/restore/plex">/mnt/user/restore/user</Config>`
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})

	t.Run("an empty template stays empty", func(t *testing.T) {
		t.Parallel()

		if got := rewriteTemplateVolumePaths(nil, []volumePathRewrite{{Old: "/a/b/c", New: "/d/e/f"}}); len(got) != 0 {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestIsPathBoundary(t *testing.T) {
	t.Parallel()

	for _, c := range []byte{'/', '"', '\'', '<', '>', ':', ',', ' ', '\t', '\r', '\n'} {
		if !isPathBoundary(c) {
			t.Errorf("isPathBoundary(%q) = false, want true", c)
		}
	}
	for _, c := range []byte{'-', '_', 'a', 'Z', '0', '.'} {
		if isPathBoundary(c) {
			t.Errorf("isPathBoundary(%q) = true, want false", c)
		}
	}
}
