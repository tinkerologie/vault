package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

func checkVersion(version string) error {
	if version == "" || version == "Unreleased" || strings.ContainsAny(version, "[]\r\n\t ") {
		return fmt.Errorf("invalid release version %q", version)
	}
	return nil
}

func extractNotes(changelog, version string) (string, error) {
	if err := checkVersion(version); err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "## [") {
			continue
		}
		if start >= 0 {
			notes := strings.TrimSpace(strings.Join(lines[start:i], "\n"))
			if notes == "" {
				return "", fmt.Errorf("release notes for %s are empty", version)
			}
			return notes + "\n", nil
		}
		heading, suffix, ok := strings.Cut(strings.TrimPrefix(line, "## ["), "]")
		if ok && (suffix == "" || (strings.HasPrefix(suffix, " - ") && strings.TrimSpace(suffix[3:]) != "")) &&
			strings.TrimPrefix(heading, "v") == version {
			start = i + 1
		}
	}
	if start < 0 {
		return "", fmt.Errorf("release notes heading for %s not found", version)
	}
	notes := strings.TrimSpace(strings.Join(lines[start:], "\n"))
	if notes == "" {
		return "", fmt.Errorf("release notes for %s are empty", version)
	}
	return notes + "\n", nil
}

type manifestInfo struct {
	version string
	start   int64
	end     int64
	notes   string
}

// Only internal entity declarations are read; encoding/xml never fetches
// external DTDs. Restrict discovery to the actual DOCTYPE, excluding CDATA.
var entityDeclaration = regexp.MustCompile(`<!ENTITY\s+([A-Za-z_][A-Za-z0-9_.:-]*)\s+(?:"([^"]*)"|'([^']*)')\s*>`)

func inspectManifest(manifest string) (manifestInfo, error) {
	var info manifestInfo
	decoder := xml.NewDecoder(strings.NewReader(manifest)) // #nosec G709 -- repository XML only; encoding/xml does not fetch external entities or recursively expand the internal entity map.
	decoder.Entity = make(map[string]string)
	var text strings.Builder
	depth, roots, blocks := 0, 0, 0
	inChanges := false
	for {
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("invalid plugin XML: %w", err)
		}
		switch token := token.(type) {
		case xml.Directive:
			if strings.HasPrefix(string(token), "DOCTYPE ") {
				if roots != 0 {
					return info, fmt.Errorf("DOCTYPE must precede PLUGIN")
				}
				for _, match := range entityDeclaration.FindAllStringSubmatch(string(token), -1) {
					if _, exists := decoder.Entity[match[1]]; exists {
						return info, fmt.Errorf("duplicate entity %q", match[1])
					}
					decoder.Entity[match[1]] = match[2] + match[3]
				}
				info.version = decoder.Entity["version"]
			}
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if token.Name.Local != "PLUGIN" || token.Name.Space != "" || roots != 1 {
					return info, fmt.Errorf("expected one PLUGIN root")
				}
			}
			if inChanges {
				return info, fmt.Errorf("CHANGES must contain Markdown text only")
			}
			if token.Name.Local == "CHANGES" {
				if depth != 2 || token.Name.Space != "" {
					return info, fmt.Errorf("CHANGES must be a direct PLUGIN child")
				}
				blocks++
				info.start = before
				inChanges = true
			}
		case xml.EndElement:
			if inChanges && depth == 2 {
				info.end = decoder.InputOffset()
				inChanges = false
			}
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return info, fmt.Errorf("text outside PLUGIN root")
			}
			if inChanges {
				text.Write(token)
			}
		}
	}
	if roots != 1 || blocks != 1 {
		return info, fmt.Errorf("expected one PLUGIN root and exactly one CHANGES block (found %d)", blocks)
	}
	info.notes = text.String()
	return info, nil
}

func replaceChanges(manifest, notes, version string) (string, error) {
	if err := checkVersion(version); err != nil {
		return "", err
	}
	info, err := inspectManifest(manifest)
	if err != nil {
		return "", err
	}
	if info.version != version {
		return "", fmt.Errorf("manifest version %q does not match release %q", info.version, version)
	}
	notes = strings.TrimSpace(strings.ReplaceAll(notes, "\r\n", "\n"))
	if notes == "" {
		return "", fmt.Errorf("release notes for %s are empty", version)
	}
	rendered := "\n## v" + version + "\n\n" + notes + "\n"
	if !utf8.ValidString(rendered) {
		return "", fmt.Errorf("release notes are not UTF-8")
	}
	for _, r := range rendered {
		valid := r == '\t' || r == '\n' || r == '\r' ||
			r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= 0x10ffff
		if !valid {
			return "", fmt.Errorf("release notes contain invalid XML character U+%04X", r)
		}
	}
	block := "<CHANGES><![CDATA[" + strings.ReplaceAll(rendered, "]]>", "]]]]><![CDATA[>") + "]]></CHANGES>"
	result := manifest[:info.start] + block + manifest[info.end:]
	decoded, err := inspectManifest(result)
	if err != nil {
		return "", err
	}
	if decoded.notes != rendered {
		return "", fmt.Errorf("CHANGES XML round trip changed release notes")
	}
	return result, nil
}
