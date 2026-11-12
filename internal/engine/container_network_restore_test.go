package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tinkerologie/vault/internal/dedup"
)

func TestContainerRestorePreservesConfiguredNetworkEndpoints(t *testing.T) {
	const inspect = `{
		"Id": "123456789abc123456789abc123456789abc123456789abc123456789abc1234",
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"NetworkMode": "primary", "Memory": 33554432},
		"NetworkSettings": {"Networks": {
			"primary": {
				"IPAMConfig": {"IPv4Address": "192.0.2.50", "IPv6Address": "2001:db8::50"},
				"MacAddress": "02:42:64:09:00:50",
				"Aliases": ["primary-alias", "123456789abc"]
			},
			"secondary": {
				"IPAddress": "198.51.100.50", "GlobalIPv6Address": "2001:db8:1::50",
				"MacAddress": "02:42:64:09:00:51", "Aliases": ["secondary-alias"], "GwPriority": 10
			}
		}}
	}`
	const template = `<Container><Name>fixture</Name><Network>primary</Network><ExtraNetworks>secondary</ExtraNetworks><MyMAC>02:42:64:09:00:50</MyMAC><Memory>32m</Memory></Container>`

	templates := withTestTemplatesDir(t)
	withUpdateStatusPath458(t, nil)
	for _, format := range []string{"classic", "chunked"} {
		t.Run(format, func(t *testing.T) {
			item := BackupItem{Name: "fixture", Type: "container"}
			templatePath := filepath.Join(templates, "my-fixture.xml")
			if err := os.Remove(templatePath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			docker, recorded := newRestoreDaemon458()
			handler := &ContainerHandler{cli: docker}
			dir := t.TempDir()
			ctx := sameServerRestore(t)
			if format == "classic" {
				writeFile458(t, filepath.Join(dir, "config.json"), inspect)
				writeFile458(t, filepath.Join(dir, "image.tar"), "fixture image")
				writeFile458(t, filepath.Join(dir, containerTemplateFile), template)
				if err := handler.Restore(ctx, item, dir, noopProgress); err != nil {
					t.Fatal(err)
				}
			} else {
				repo, _, closeRepo := dedup.NewTestRepoForEngine(t)
				defer closeRepo()
				manifest, err := repo.PutManifest(item.Name, dedup.Manifest{Files: map[string]dedup.ManifestEntry{
					containerInspectKey:  chunkTestFile(t, repo, filepath.Join(dir, "inspect.json"), inspect),
					containerTemplateKey: chunkTestFile(t, repo, filepath.Join(dir, "template.xml"), template),
				}})
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := handler.RestoreChunked(ctx, item, repo, manifest, "", noopProgress); err != nil {
					t.Fatal(err)
				}
			}
			if len(recorded.created) != 1 {
				t.Fatalf("expected one Docker create, got %d", len(recorded.created))
			}
			created := recorded.created[0]
			if created.HostConfig.Memory != 33554432 || created.HostConfig.NetworkMode != "primary" {
				t.Fatalf("unexpected memory or primary network: %+v", created.HostConfig)
			}
			endpoints := created.NetworkingConfig.EndpointsConfig
			primary, secondary := endpoints["primary"], endpoints["secondary"]
			if len(endpoints) != 2 || primary == nil || secondary == nil {
				t.Fatalf("expected two Docker endpoints, got %v", endpoints)
			}
			if primary.IPAMConfig == nil || primary.IPAMConfig.IPv4Address.String() != "192.0.2.50" ||
				primary.IPAMConfig.IPv6Address.String() != "2001:db8::50" {
				t.Errorf("primary static addresses = %+v", primary.IPAMConfig)
			}
			if primary.MacAddress.String() != "02:42:64:09:00:50" || len(secondary.MacAddress) != 0 ||
				secondary.IPAMConfig != nil || secondary.IPAddress.IsValid() || secondary.GlobalIPv6Address.IsValid() {
				t.Errorf("expected only the configured primary MAC and no runtime addresses: primary=%+v secondary=%+v", primary, secondary)
			}
			if !slices.Equal(primary.Aliases, []string{"primary-alias"}) ||
				!slices.Equal(secondary.Aliases, []string{"secondary-alias"}) || secondary.GwPriority != 10 {
				t.Errorf("network aliases or gateway priority differ: primary=%+v secondary=%+v", primary, secondary)
			}
			if got := readFile458(t, templatePath); got != template {
				t.Errorf("DockerMan template changed: %q", got)
			}
		})
	}
}

func TestRestorePrimaryMACRequiresExplicitConfiguration(t *testing.T) {
	const configured = "02:42:64:09:00:50"
	const generated = "02:42:64:09:00:51"
	const template = `<Container><Network>primary</Network><MyMAC>` + configured + `</MyMAC></Container>`
	for _, tc := range []struct {
		name, template, network, observed, legacy, want string
	}{
		{name: "stopped container", template: template, network: "primary", want: configured},
		{name: "stale template", template: template, network: "primary", observed: generated},
		{name: "stale template with legacy MAC", template: template, network: "primary", observed: generated, legacy: generated, want: generated},
		{name: "other network", template: template, network: "secondary", observed: configured},
		{name: "generated MAC only", network: "primary", observed: generated},
		{name: "legacy explicit MAC", network: "primary", observed: configured, legacy: configured, want: configured},
		{name: "malformed template", template: "<Container>", network: "primary"},
		{name: "invalid MAC", template: `<Container><Network>primary</Network><MyMAC>invalid</MyMAC></Container>`, network: "primary", legacy: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := restorePrimaryMAC([]byte(tc.template), tc.network, tc.observed, tc.legacy).String()
			if got != tc.want {
				t.Errorf("MAC = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRestoreNetworkingConfigLegacyNetworkModes(t *testing.T) {
	const configuredMAC = "02:42:64:09:00:50"
	for _, tc := range []struct {
		mode, endpoint, wantMAC string
	}{
		{mode: "", endpoint: "bridge", wantMAC: configuredMAC},
		{mode: "default", endpoint: "bridge", wantMAC: configuredMAC},
		{mode: "host", endpoint: "host"},
		{mode: "none", endpoint: "none"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var inspect restoreInspect
			inspect.HostConfig.NetworkMode = tc.mode
			inspect.Config.MacAddress = configuredMAC
			inspect.NetworkSettings.Networks = map[string]restoreNetworkEndpoint{tc.endpoint: {}}
			endpoints := inspect.restoreNetworkingConfig(nil).EndpointsConfig
			got := endpoints[tc.endpoint]
			if len(endpoints) != 1 || got == nil || got.MacAddress.String() != tc.wantMAC {
				t.Errorf("restored network %q with unexpected settings: %+v", tc.endpoint, got)
			}
		})
	}
	if got := (restoreInspect{}).restoreNetworkingConfig(nil).EndpointsConfig; len(got) != 0 {
		t.Errorf("backup without network metadata produced endpoints: %v", got)
	}
}

func TestRestoreNetworkingConfigRejectsInvalidStaticAddresses(t *testing.T) {
	const saved = `{
		"Id": "123456789abc123456789abc123456789abc123456789abc123456789abc1234",
		"NetworkSettings": {"Networks": {
			"ipv6-only": {"IPAMConfig": {"IPv4Address": "invalid", "IPv6Address": "2001:db8::50"}},
			"mapped": {"IPAMConfig": {"IPv6Address": "::ffff:192.0.2.50"}, "Aliases": [
				"123456789abc123456789abc123456789abc123456789abc123456789abc1234", "service"
			]},
			"zoned": {"IPAMConfig": {"IPv6Address": "fe80::1%eth0"}}
		}}
	}`
	inspect := inspectFromJSON(t, saved)
	endpoints := inspect.restoreNetworkingConfig(nil).EndpointsConfig
	ipam := endpoints["ipv6-only"].IPAMConfig
	if ipam == nil || ipam.IPv4Address.IsValid() || ipam.IPv6Address.String() != "2001:db8::50" {
		t.Errorf("expected only the valid IPv6 address, got %+v", ipam)
	}
	if endpoints["mapped"].IPAMConfig != nil || endpoints["zoned"].IPAMConfig != nil {
		t.Error("restore replayed mapped or scoped IPv6 as a static address")
	}
	if got := endpoints["mapped"].Aliases; !slices.Equal(got, []string{"service"}) {
		t.Errorf("expected only the configured alias, got %v", got)
	}
	inspect.ID = ""
	if got := inspect.restoreNetworkingConfig(nil).EndpointsConfig["mapped"].Aliases; len(got) != 2 {
		t.Errorf("expected both aliases when the backup omits the container ID, got %v", got)
	}
}
