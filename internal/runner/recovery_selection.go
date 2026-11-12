package runner

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

type Status string

const (
	Confirmed Status = "confirmed"
	Missing   Status = "missing"
	Unknown   Status = "unknown"
	Broken    Status = "broken"
)

type ItemSelection struct {
	ItemType string `json:"item_type" jsonschema:"Backup Item type: folder, container, or vm"`
	ItemName string `json:"item_name" jsonschema:"Exact Backup Item name"`
}

var ErrInvalidSelection = errors.New("item_type must be folder, container, or vm, and item_name must be a valid Backup Item name")

func (s ItemSelection) Validate() error {
	if (s.ItemType != "folder" && s.ItemType != "container" && s.ItemType != "vm") ||
		len(s.ItemName) > 4096 || strings.TrimSpace(s.ItemName) == "" ||
		!utf8.ValidString(s.ItemName) || strings.IndexFunc(s.ItemName, unicode.IsControl) >= 0 {
		return ErrInvalidSelection
	}
	return nil
}

func (s ItemSelection) Redacted() ItemSelection {
	s.ItemName = RedactRecoveryLabel(s.ItemName)
	return s
}

// PointIdentity exposes the identifiers and timestamp needed to select a
// Restore Point without including its storage path or metadata.
type PointIdentity struct {
	ID         int64     `json:"id"`
	JobID      int64     `json:"job_id"`
	BackupType string    `json:"backup_type"`
	CreatedAt  time.Time `json:"created_at"`
}

func IdentifyPoint(p db.RestorePoint) PointIdentity {
	backupType := "unknown"
	switch p.BackupType {
	case "full", "incremental", "differential":
		backupType = p.BackupType
	}
	return PointIdentity{ID: p.ID, JobID: p.JobID, BackupType: backupType, CreatedAt: p.CreatedAt}
}

// Membership derives existence only from the Restore Point's recorded items.
// An explicitly recorded different type never counts as a match.
func Membership(point db.RestorePoint, item ItemSelection) Status {
	names, known := point.BackedUpItems()
	if !known {
		return Unknown
	}
	if _, found := names[item.ItemName]; !found || recordedItemType(point, item) == Missing {
		return Missing
	}
	return Confirmed
}

func recordedItemType(point db.RestorePoint, item ItemSelection) Status {
	var meta struct {
		ItemTypes     map[string]string `json:"item_types"`
		VMCheckpoints map[string]string `json:"vm_checkpoints"`
		ManifestItems []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"manifest_items"`
	}
	if json.Unmarshal([]byte(point.Metadata), &meta) != nil {
		return Unknown
	}
	if kind := meta.ItemTypes[item.ItemName]; kind != "" {
		if kind == item.ItemType {
			return Confirmed
		}
		return Missing
	}
	if meta.VMCheckpoints[item.ItemName] != "" {
		if item.ItemType == "vm" {
			return Confirmed
		}
		return Missing
	}
	result := Unknown
	for _, entry := range meta.ManifestItems {
		if entry.Name == item.ItemName && entry.Type != "" {
			if entry.Type == item.ItemType {
				return Confirmed
			}
			result = Missing
		}
	}
	return result
}

// Some older folder records use the source path as the item name. Labels
// from imported records can also contain URLs or a pasted Assistant Key.
func RedactRecoveryLabel(label string) string {
	candidate := strings.TrimSpace(label)
	if strings.HasPrefix(candidate, "/") || strings.Contains(candidate, "://") || strings.Contains(candidate, crypto.AssistantKeyPrefix) {
		return "[redacted]"
	}
	return label
}
