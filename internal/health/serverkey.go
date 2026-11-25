package health

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

// ServerKeySavedSetting records when vault.key was last downloaded, or when the
// operator confirmed keeping a copy elsewhere (#714).
const ServerKeySavedSetting = "server_key_saved_at"

// RecordServerKeyCopy notes that a copy of this key now exists off the server:
// the time and the key's fingerprint, so a later regenerated key isn't covered.
func RecordServerKeyCopy(database *db.DB, key []byte, now time.Time) error {
	return database.SetSetting(ServerKeySavedSetting, now.UTC().Format(time.RFC3339)+" "+crypto.ServerKeyFingerprint(key))
}

// ServerKeyCopyRecorded reports whether a copy of the current vault.key is
// known to exist off the server. The record must hold a valid time and, once
// the daemon has published the loaded key's fingerprint, that same fingerprint.
// Records from before fingerprints count only while the key is unknown.
func ServerKeyCopyRecorded(database *db.DB) bool {
	saved, _ := database.GetSetting(ServerKeySavedSetting, "")
	fields := strings.Fields(saved)
	if len(fields) == 0 || len(fields) > 2 {
		return false
	}
	if _, err := time.Parse(time.RFC3339, fields[0]); err != nil {
		return false
	}
	current, _ := database.GetSetting(db.ServerKeyFingerprintSetting, "")
	if current == "" {
		return true
	}
	return len(fields) == 2 && fields[1] == current
}

// serverKeyAtRisk reports whether vault.key is the only way to open this
// server's deduplicated backups and no copy is known to exist off the server
// (#714). Since #451 a backup passphrase escrows the dedup master key, so the
// key file only matters while no passphrase is set; its copy, on the same flash
// drive Vault protects, would be lost with the server.
func serverKeyAtRisk(database *db.DB, jobs []db.Job) bool {
	if ServerKeyCopyRecorded(database) {
		return false
	}
	dests, err := database.ListStorageDestinations()
	if err != nil {
		log.Printf("health summary: listing storage destinations: %v", err)
		return false
	}
	dedup := map[int64]bool{}
	for _, dest := range dests {
		if dest.DedupEnabled {
			dedup[dest.ID] = true
		}
	}
	// A backup passphrase protects a destination only once the runner has
	// confirmed its escrow for that passphrase (written on the next backup).
	hash, _ := database.GetSetting("encryption_passphrase_hash", "")
	for _, job := range jobs {
		if !dedup[job.StorageDestID] {
			continue
		}
		if hash == "" {
			return true
		}
		if confirmed, _ := database.GetSetting(db.DedupEscrowConfirmedPrefix+strconv.FormatInt(job.StorageDestID, 10), ""); confirmed != hash {
			return true
		}
	}
	return false
}
