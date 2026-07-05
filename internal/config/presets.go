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
	// Tdarr stores its database (DB2), plugins, samples and config JSON under
	// /app/server and /app/configs — both are kept so settings and statistics
	// restore cleanly. Only the transcode cache (/temp) and logs are dropped.
	// The media library is not excluded here: when it is bind-mounted from an
	// Unraid shared-data path (e.g. /mnt/user/media) the engine's shared-data
	// heuristic auto-skips it by host source; a media mount from an unusual
	// host path can be unchecked in the job editor. The same key matches the
	// tdarr_node image via substring.
	"tdarr": {
		"/temp",
		"/app/logs",
		"*.log",
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
	// Immich stores original assets (upload/library/profile) and its built-in
	// database dumps (backups) that must always be kept, plus regeneratable
	// derived content (thumbs, encoded-video) that is safe to skip and is
	// re-created on demand after a restore. The official immich-app image mounts
	// the media root at /data; the imagegenius fork uses /photos — both layouts
	// are covered here. NOTE: Immich's PostgreSQL database runs in a SEPARATE
	// container, so this preset does not capture it — see ContainerPresetMeta
	// for the database advisory surfaced in the job editor.
	"immich": {
		"/data/thumbs",
		"/data/encoded-video",
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
	// Host-mount monitoring agents — these bind-mount the host root and/or
	// /proc, /sys, /var/run/docker.sock. Without these exclusions the engine
	// would attempt to recurse the entire host filesystem (including the
	// Unraid array into itself) and the job will hang indefinitely
	// (issue #70).
	"telegraf": {
		"/rootfs",
		"/hostfs",
		"/var/run/docker.sock",
		"/var/run/utmp",
		"/run/udev",
	},
	"glances": {
		"/rootfs",
		"/var/run/docker.sock",
		"/etc/os-release",
	},
	"netdata": {
		"/host",
		"/hostfs",
		"/etcfs",
		"/var/run/docker.sock",
		"/var/cache/netdata",
		"/var/log/netdata",
	},
	"cadvisor": {
		"/rootfs",
		"/var/run/docker.sock",
		"/var/lib/docker",
		"/cgroup",
		"/sys/fs/cgroup",
	},
	"node-exporter": {
		"/host",
		"/rootfs",
		"/hostfs",
	},
	"node_exporter": {
		"/host",
		"/rootfs",
		"/hostfs",
	},
	"scrutiny": {
		"/run/udev",
		"/var/run/docker.sock",
	},
	"prometheus": {
		"/prometheus/wal",
		"/prometheus/chunks_head",
		"*.log",
	},
	"loki": {
		"/loki/wal",
		"/loki/chunks",
		"*.log",
	},
	"victoriametrics": {
		"/victoria-metrics-data/tmp",
		"*.log",
	},
	"uptime-kuma": {
		"/app/data/upload",
		"*.log",
	},

	// Docker management / agent containers — bind-mount the Docker socket
	// and often /var/lib/docker. Backing up the socket fails the tar header
	// stage; /var/lib/docker is huge and never useful to back up via these
	// containers.
	"dockhand": {
		"/var/run/docker.sock",
	},
	"watchtower": {
		"/var/run/docker.sock",
	},
	"diun": {
		"/var/run/docker.sock",
	},
	"dozzle": {
		"/var/run/docker.sock",
	},
	"autoheal": {
		"/var/run/docker.sock",
	},
	"docker-socket-proxy": {
		"/var/run/docker.sock",
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
		"/var/run/docker.sock",
		"*.log",
	},
	"syncthing": {
		"/config/index-*",
	},
}

// PresetMeta carries optional advisory notes and warnings for a container
// preset. It is kept in a map separate from ContainerExclusionPresets so the
// paths map's []string contract (and GetExclusionPreset) stays unchanged; the
// API handler merges this metadata into its response only when present.
type PresetMeta struct {
	// Notes are informational and safe to show at info level.
	Notes []string
	// Warnings flag a caveat the user should act on (e.g. a database that this
	// filesystem backup does not capture).
	Warnings []string
}

// ContainerPresetMeta maps the same lower-case image substrings used by
// ContainerExclusionPresets to advisory metadata. Only apps that need a caveat
// have an entry.
var ContainerPresetMeta = map[string]PresetMeta{
	"immich": {
		Warnings: []string{
			"Immich's PostgreSQL database runs in a separate container, so this filesystem backup does not capture it. Enable Immich's built-in database backups (Administration → Settings → Backup Settings) — it writes .sql.gz dumps into the backups/ folder inside your media root, which this job keeps — or back up the Postgres container as its own Vault job.",
		},
		Notes: []string{
			"Original photos and videos (upload, library, profile) and the built-in database dumps (backups) are always backed up; regeneratable thumbnails and re-encoded video are excluded and are re-created on demand after a restore.",
			"Both the official immich-app image (media root at /data) and the imagegenius fork (/photos) layouts are covered.",
		},
	},
}

// presetKeys holds sorted keys for deterministic iteration order.
var presetKeys []string

// presetMetaKeys holds sorted ContainerPresetMeta keys for deterministic
// substring matching in GetPresetMeta.
var presetMetaKeys []string

func init() {
	presetKeys = make([]string, 0, len(ContainerExclusionPresets))
	for key := range ContainerExclusionPresets {
		presetKeys = append(presetKeys, key)
	}
	sort.Strings(presetKeys)

	presetMetaKeys = make([]string, 0, len(ContainerPresetMeta))
	for key := range ContainerPresetMeta {
		presetMetaKeys = append(presetMetaKeys, key)
	}
	sort.Strings(presetMetaKeys)
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

// GetPresetMeta returns advisory metadata for a container image using the same
// case-insensitive substring matching as GetExclusionPreset. The bool is false
// when no metadata is defined for the image.
func GetPresetMeta(image string) (PresetMeta, bool) {
	if image == "" {
		return PresetMeta{}, false
	}
	lower := strings.ToLower(image)
	for _, key := range presetMetaKeys {
		if strings.Contains(lower, key) {
			return ContainerPresetMeta[key], true
		}
	}
	return PresetMeta{}, false
}
