package docsmeta

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// appGroupOrder is the render order of user-facing groups on the app-settings
// page. GroupInternal is deliberately absent: those keys are secrets/bookkeeping
// and must never appear in the published reference.
var appGroupOrder = []Group{GroupGeneral, GroupBackup, GroupAnomaly, GroupNotifications}

// emptyDefault renders an empty Default cell so the table stays readable.
const emptyDefault = "_(empty)_"

// RenderAppSettings renders the full app/daemon settings page: a top-level
// heading followed by one "## <Group>" section per user-facing group, each with
// a markdown table whose rows are sorted by key. Internal keys are excluded.
func RenderAppSettings() string {
	var b strings.Builder
	b.WriteString("# App / Daemon Settings\n\n")
	b.WriteString("Settings persisted in Vault's settings table, grouped by area. ")
	b.WriteString("Defaults are the built-in values used when a setting is unset.\n")

	for _, g := range appGroupOrder {
		b.WriteString("\n## ")
		b.WriteString(string(g))
		b.WriteString("\n\n")
		writeSettingsTable(&b, settingsForGroup(g))
	}
	return b.String()
}

// RenderNotifications renders a standalone page for the Notifications group.
func RenderNotifications() string {
	var b strings.Builder
	b.WriteString("# Notifications\n\n")
	b.WriteString("Settings controlling outbound notification delivery.\n\n")
	writeSettingsTable(&b, settingsForGroup(GroupNotifications))
	return b.String()
}

// settingsForGroup returns the settings in a group, sorted by key.
func settingsForGroup(g Group) []SettingDoc {
	var out []SettingDoc
	for _, s := range AppSettings {
		if s.Group == g {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func writeSettingsTable(b *strings.Builder, settings []SettingDoc) {
	b.WriteString("| Setting | Type | Default | Description |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, s := range settings {
		def := emptyDefault
		if s.Default != "" {
			def = "`" + s.Default + "`"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", s.Key, s.Type, def, s.Description)
	}
}

// RenderStruct renders a config-struct reference page by reflecting over v. It
// emits a "# <title>" heading and a table of the struct's exported, user-facing
// fields (Field | Type | JSON key | Description). Unexported fields and fields
// listed in InternalFields are skipped; descriptions come from FieldDocs.
func RenderStruct(title string, v any) string {
	rt := reflect.TypeOf(v)
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}

	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString("| Field | Type | JSON key | Description |\n")
	b.WriteString("| --- | --- | --- | --- |\n")

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		qualified := rt.Name() + "." + f.Name
		if InternalFields[qualified] {
			continue
		}
		jsonKey := f.Tag.Get("json")
		if comma := strings.IndexByte(jsonKey, ','); comma >= 0 {
			jsonKey = jsonKey[:comma]
		}
		desc := FieldDocs[qualified]
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s |\n", f.Name, f.Type.String(), jsonKey, desc)
	}
	return b.String()
}

// RenderLocal renders the local-storage page. The local config is an anonymous
// inline struct (struct{ Path string }) in the storage factory, so there is no
// named type to reflect over; this page is hand-rolled with its single field.
func RenderLocal() string {
	desc := FieldDocs["LocalConfig.Path"]
	if desc == "" {
		desc = "Absolute path on the host filesystem where backups are stored."
	}
	var b strings.Builder
	b.WriteString("# Storage: Local\n\n")
	b.WriteString("| Field | Type | JSON key | Description |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	fmt.Fprintf(&b, "| Path | string | `path` | %s |\n", desc)
	return b.String()
}
