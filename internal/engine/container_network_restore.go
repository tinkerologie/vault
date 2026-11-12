package engine

import (
	"encoding/xml"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/network"
)

// restoreNetworkEndpoint carries configurable endpoint fields. The observed
// MAC is used only to check whether the saved DockerMan template still agrees
// with the container that was backed up.
type restoreNetworkEndpoint struct {
	IPAMConfig *struct {
		IPv4Address string `json:"IPv4Address"`
		IPv6Address string `json:"IPv6Address"`
	} `json:"IPAMConfig"`
	Aliases    []string `json:"Aliases"`
	GwPriority int      `json:"GwPriority"`
	MacAddress string   `json:"MacAddress"`
}

func (ri restoreInspect) restoreNetworkingConfig(template []byte) *network.NetworkingConfig {
	config := &network.NetworkingConfig{}
	if len(ri.NetworkSettings.Networks) == 0 {
		return config
	}
	primary := ri.HostConfig.NetworkMode
	if primary == "" || primary == network.NetworkDefault {
		primary = network.NetworkBridge
	}
	backupID := ShortID(ri.ID)
	config.EndpointsConfig = make(map[string]*network.EndpointSettings, len(ri.NetworkSettings.Networks))
	for name, saved := range ri.NetworkSettings.Networks {
		endpoint := &network.EndpointSettings{
			// Older Docker APIs included the old container's short ID in
			// Aliases. Docker generates the new ID on create.
			Aliases: slices.DeleteFunc(slices.Clone(saved.Aliases), func(alias string) bool {
				return ri.ID != "" && (alias == backupID || alias == ri.ID)
			}),
			GwPriority: saved.GwPriority,
		}
		if saved.IPAMConfig != nil {
			v4, _ := netip.ParseAddr(saved.IPAMConfig.IPv4Address)
			v6, _ := netip.ParseAddr(saved.IPAMConfig.IPv6Address)
			ipam := &network.EndpointIPAMConfig{}
			if v4.Is4() {
				ipam.IPv4Address = v4
			}
			if v6.Is6() && !v6.Is4In6() && v6.Zone() == "" {
				ipam.IPv6Address = v6
			}
			if ipam.IPv4Address.IsValid() || ipam.IPv6Address.IsValid() {
				endpoint.IPAMConfig = ipam
			}
		}
		if name == primary && primary != network.NetworkHost && primary != network.NetworkNone {
			endpoint.MacAddress = restorePrimaryMAC(template, primary, saved.MacAddress, ri.Config.MacAddress)
		}
		config.EndpointsConfig[name] = endpoint
	}
	return config
}

func restorePrimaryMAC(template []byte, primary, observed, legacy string) network.HardwareAddr {
	var dockerMan struct {
		XMLName xml.Name `xml:"Container"`
		Network string   `xml:"Network"`
		MyMAC   string   `xml:"MyMAC"`
	}
	if len(template) > 0 && xml.Unmarshal(template, &dockerMan) == nil &&
		strings.EqualFold(strings.TrimSpace(dockerMan.Network), primary) {
		mac, _ := net.ParseMAC(strings.TrimSpace(dockerMan.MyMAC))
		if len(mac) == 6 && (observed == "" || strings.EqualFold(observed, mac.String())) {
			return network.HardwareAddr(mac)
		}
	}

	// Older inspect responses could carry an explicit primary MAC separately.
	// A modern inspect response only exposes the operational MAC, which Docker
	// may have generated. Never replay it without an explicit setting.
	mac, _ := net.ParseMAC(legacy)
	if len(mac) == 6 {
		return network.HardwareAddr(mac)
	}
	return nil
}
