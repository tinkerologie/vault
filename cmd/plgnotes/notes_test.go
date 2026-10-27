package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureManifest = `<?xml version="1.0"?>
<!DOCTYPE PLUGIN [
<!ENTITY name "vault">
<!ENTITY version "2026.10.03">
]>
<PLUGIN name="&name;" version="&version;">
<CHANGES><![CDATA[See full changelog at: https://example.org/CHANGELOG.md]]></CHANGES>
<FILE Run="/bin/bash"><INLINE><![CDATA[echo '<CHANGES>shell literal</CHANGES>' && test "$x" -gt 0
]]></INLINE></FILE>
</PLUGIN>
`

func TestExtractNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, changelog, version, want string
	}{
		{"prefixed", "## [Unreleased]\n- future\n## [v2026.10.03] - 2026-10-23\n\n### Fixed\n\n- current\n\n## [v2026.10.02]\n- old", "2026.10.03", "### Fixed\n\n- current\n"},
		{"plain", "## [2026.10.03]\n- current", "2026.10.03", "- current\n"},
		{"adjacent", "## [v2026.10.0]\n- adjacent\n## [v2026.10.03]\n- current", "2026.10.03", "- current\n"},
		{"crlf", "## [v2026.10.03]\r\n\r\n- current\r\n\r\n", "2026.10.03", "- current\n"},
		{"literal dot", "## [v2026x10x03]\n- wrong\n## [v2026.10.03]\n- current", "2026.10.03", "- current\n"},
		{"missing", "## [Unreleased]\n- future", "2026.10.03", ""},
		{"empty", "## [v2026.10.03]\n", "2026.10.03", ""},
		{"empty before next", "## [v2026.10.03]\n \t\n## [v2026.10.02]\n- old", "2026.10.03", ""},
		{"bad suffix", "## [v2026.10.03]suffix\n- wrong", "2026.10.03", ""},
		{"empty date", "## [v2026.10.03] - \n- wrong", "2026.10.03", ""},
		{"unreleased", "## [Unreleased]\n- future", "Unreleased", ""},
		{"empty version", "", "", ""},
		{"newline version", "", "2026\n10", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractNotes(tc.changelog, tc.version)
			if (err != nil) != (tc.want == "") || got != tc.want {
				t.Fatalf("extractNotes = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReplaceChanges(t *testing.T) {
	t.Parallel()
	notes := "### Fixed\n\n- Preserve & < > ]]> and literal &version;.\n- Literal <CHANGES> and </CHANGES>.\n- Unicode: 日本語 🚀\n"
	got, err := replaceChanges(fixtureManifest, notes, "2026.10.03")
	if err != nil {
		t.Fatal(err)
	}
	info, err := inspectManifest(got)
	if err != nil || info.notes != "\n## v2026.10.03\n\n"+notes {
		t.Fatalf("round trip = %q, %v", info.notes, err)
	}
	if strings.Contains(got, "See full changelog") {
		t.Fatal("previous notes remain")
	}
	oldInfo, err := inspectManifest(fixtureManifest)
	if err != nil {
		t.Fatal(err)
	}
	if got[:info.start] != fixtureManifest[:oldInfo.start] || got[info.end:] != fixtureManifest[oldInfo.end:] {
		t.Fatal("changed bytes outside CHANGES, including shell CDATA")
	}
	again, err := replaceChanges(got, notes, "2026.10.03")
	if err != nil || again != got {
		t.Fatalf("not idempotent: %v", err)
	}
	next := strings.Replace(got, `<!ENTITY version "2026.10.03">`, `<!ENTITY version "2026.10.04">`, 1)
	next, err = replaceChanges(next, "- next release", "2026.10.04")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(next, "Preserve &") || strings.Contains(next, "## v2026.10.03") || !strings.Contains(next, "- next release") {
		t.Fatal("next release did not replace the previous release")
	}
}

func TestReplaceChangesRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, manifest, notes, version string }{
		{"missing changes", strings.Replace(fixtureManifest, "<CHANGES><![CDATA[See full changelog at: https://example.org/CHANGELOG.md]]></CHANGES>", "", 1), "- fix", "2026.10.03"},
		{"multiple changes", strings.Replace(fixtureManifest, "</PLUGIN>", "<CHANGES>extra</CHANGES></PLUGIN>", 1), "- fix", "2026.10.03"},
		{"wrong version", fixtureManifest, "- fix", "2026.10.04"},
		{"empty notes", fixtureManifest, " \n\t", "2026.10.03"},
		{"control", fixtureManifest, "- bad\x01", "2026.10.03"},
		{"invalid utf8", fixtureManifest, "- bad\xff", "2026.10.03"},
		{"invalid version", fixtureManifest, "- fix", ""},
		{"invalid XML", strings.TrimSuffix(fixtureManifest, "</PLUGIN>\n"), "- fix", "2026.10.03"},
		{"wrong root", "<OTHER><CHANGES>old</CHANGES></OTHER>", "- fix", "2026.10.03"},
		{"two roots", fixtureManifest + "<PLUGIN/>", "- fix", "2026.10.03"},
		{"outside text", fixtureManifest + "text", "- fix", "2026.10.03"},
		{"nested markup", strings.Replace(fixtureManifest, "<CHANGES>", "<CHANGES><b/>", 1), "- fix", "2026.10.03"},
		{"nested changes", "<PLUGIN><FILE><CHANGES>old</CHANGES></FILE></PLUGIN>", "- fix", "2026.10.03"},
		{"duplicate entity", strings.Replace(fixtureManifest, "<!ENTITY name", "<!ENTITY version '2026.10.03'>\n<!ENTITY name", 1), "- fix", "2026.10.03"},
		{"late doctype", strings.Replace(fixtureManifest, "</PLUGIN>", "<!DOCTYPE PLUGIN []></PLUGIN>", 1), "- fix", "2026.10.03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := replaceChanges(tc.manifest, tc.notes, tc.version); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRealManifest(t *testing.T) {
	t.Parallel()
	manifest, err := os.ReadFile(filepath.Join("..", "..", "plugin", "vault.plg"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := inspectManifest(string(manifest))
	if err != nil {
		t.Fatal(err)
	}
	got, err := replaceChanges(string(manifest), "### Fixed\n\n- Fixture < & ]]> &version;.\n", info.version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectManifest(got); err != nil {
		t.Fatal(err)
	}
}
