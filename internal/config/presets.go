package config

import (
	"sort"
	"strings"
)

// ContainerExclusionPresets maps container image name substrings to recommended
// exclusion paths. Keys are matched case-insensitively against the full image name.
var ContainerExclusionPresets = map[string][]string{
	// Media Servers
	"plex": {
		"/config/Library/Application Support/Plex Media Server/Cache",
		"/config/Library/Application Support/Plex Media Server/Codecs",
		"/config/Library/Application Support/Plex Media Server/Crash Reports",
		"/config/Library/Application Support/Plex Media Server/Logs",
		"/config/Library/Application Support/Plex Media Server/Diagnostics",
		"/config/Library/Application Support/Plex Media Server/Drivers",
		"/config/Library/Application Support/Plex Media Server/Plug-in Support/Caches",
	},
	"emby": {
		"/config/cache",
		"/config/logs",
		"/config/transcodes",
	},
	"jellyfin": {
		"/config/transcodes",
		"/cache",
		"/config/log",
	},

	// Media Management (*arr stack)
	"sonarr": {
		"/config/logs",
		"/config/Backups",
		"/config/MediaCover",
		"/config/UpdateLogs",
	},
	"radarr": {
		"/config/logs",
		"/config/Backups",
		"/config/MediaCover",
		"/config/UpdateLogs",
	},
	"lidarr": {
		"/config/logs",
		"/config/Backups",
		"/config/MediaCover",
		"/config/UpdateLogs",
	},
	"prowlarr": {
		"/config/logs",
		"/config/Backups",
		"/config/UpdateLogs",
	},
	"readarr": {
		"/config/logs",
		"/config/Backups",
		"/config/MediaCover",
		"/config/UpdateLogs",
	},
	"bazarr": {
		"/config/log",
		"/config/backup",
	},
	"seerr": {
		"/app/config/logs",
		"/app/config/cache",
	},

	// Download Clients
	"sabnzbd": {
		"/config/logs",
		"/config/Downloads/incomplete",
		"*.log",
	},
	"nzbget": {
		"/config/intermediate",
		"*.log",
	},
	"qbittorrent": {
		"/config/.cache",
		"/config/qBittorrent/logs",
		"*.log",
	},
	"deluge": {
		"/config/logs",
		"*.log",
	},
	"transmission": {
		"/config/logs",
		"*.log",
	},

	// Transcoding/Processing
	"tdarr": {
		"/temp",
		"/app/logs",
	},
	"unmanic": {
		"/tmp/unmanic",
	},

	// Home Automation
	"homeassistant": {
		"home-assistant_v2.db",
		"home-assistant_v2.db-shm",
		"home-assistant_v2.db-wal",
		"/config/backups",
		"/config/tts",
		"*.log",
	},
	"home-assistant": {
		"home-assistant_v2.db",
		"home-assistant_v2.db-shm",
		"home-assistant_v2.db-wal",
		"/config/backups",
		"/config/tts",
		"*.log",
	},

	// Photo/Video Management
	"immich": {
		"/photos/thumbs",
		"/photos/encoded-video",
	},
	"photoprism": {
		"/photoprism/cache/thumbnails",
	},
	"frigate": {
		"/media/frigate/recordings",
		"/media/frigate/clips",
		"/tmp/cache",
	},

	// Cloud/Productivity
	"nextcloud": {
		"/var/www/html/data/appdata_*/preview",
		"/var/www/html/data/appdata_*/cache",
	},
	"vaultwarden": {
		"/data/icon_cache",
		"/data/tmp",
	},
	"paperless": {
		"/usr/src/paperless/data/index",
		"/usr/src/paperless/media/documents/thumbnails",
		"/usr/src/paperless/consume",
	},

	// Monitoring
	"grafana": {
		"/var/lib/grafana/plugins",
		"/var/lib/grafana/png",
		"*.log",
	},
	"influxdb": {
		"*.log",
		"/var/lib/influxdb/wal",
	},

	// DNS/Ad-blocking
	"pihole": {
		"/etc/pihole/pihole-FTL.db",
		"/etc/pihole/macvendor.db",
		"/var/log/pihole",
	},
	"adguardhome": {
		"/opt/adguardhome/work/data/querylog.json",
		"/opt/adguardhome/work/data/stats.db",
		"/opt/adguardhome/work/data/filters",
		"/opt/adguardhome/work/data/sessions.db",
	},

	// Reverse Proxies
	"nginx-proxy-manager": {
		"/data/logs",
	},

	// Misc
	"portainer": {
		"*.log",
	},
	"syncthing": {
		"/config/index-*",
	},
}

// presetKeys holds sorted keys for deterministic iteration order.
var presetKeys []string

func init() {
	presetKeys = make([]string, 0, len(ContainerExclusionPresets))
	for key := range ContainerExclusionPresets {
		presetKeys = append(presetKeys, key)
	}
	sort.Strings(presetKeys)
}

// GetExclusionPreset returns recommended exclusion paths for a container image.
// It matches the image name against known presets using case-insensitive substring
// matching. Returns nil if no preset matches.
func GetExclusionPreset(image string) []string {
	if image == "" {
		return nil
	}
	lower := strings.ToLower(image)
	for _, key := range presetKeys {
		if strings.Contains(lower, key) {
			return ContainerExclusionPresets[key]
		}
	}
	return nil
}
