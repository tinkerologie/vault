package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tinkerologie/vault/internal/safepath"
)

const VMRestoreSidecarLimit = 4 << 20

var (
	ErrVMBackupEvidenceUnknown   = errors.New("saved VM configuration or disk inventory cannot be confirmed")
	ErrVMBackupArtifactMissing   = errors.New("a recorded VM disk or NVRAM artifact is missing")
	ErrVMRestoreTargetDisallowed = errors.New("a VM disk or NVRAM target is outside the restore policy")
	ErrVMRestoreTargetCollision  = errors.New("VM disk and NVRAM targets overlap")
	ErrVMRestoreIdentityChanged  = errors.New("the saved VM identity does not match the selected VM or existing domain")
	ErrVMRestoreTargetInUse      = errors.New("a VM restore target is used by another domain")
	ErrVMRestoreHostUnavailable  = errors.New("the local VM host or target could not be checked")
	ErrVMReviewedEffectsChanged  = errors.New("reviewed VM restore effects changed")
)

// VMBackupEvidence is the execution inventory of a single saved VM step.
// Raw XML and readiness addresses remain in the sidecars.
type VMBackupEvidence struct {
	Name, UUID, BackupType, Checkpoint, ParentCheckpoint string
	Disks                                                []VMBackupDisk
	NVRAMFile                                            string
}

type VMBackupDisk struct {
	Target, BackupFile, Format string
}

// VMRestoreEffects contains only the fields that an operator needs to review.
type VMRestoreEffects struct {
	Name              string                `json:"name"`
	UUID              string                `json:"uuid"`
	Destination       string                `json:"destination,omitempty"`
	Disks             []VMRestoreDiskEffect `json:"disks"`
	NVRAM             *VMRestoreDiskEffect  `json:"nvram,omitempty"`
	UnsupportedDisks  []VMUnsupportedDisk   `json:"unsupported_disks,omitempty"`
	Domain            VMRestoreDomainEffect `json:"domain"`
	StartAfterRestore bool                  `json:"start_after_restore"`
	ReadinessMode     string                `json:"readiness_mode"`
	ReadinessTimeout  int                   `json:"readiness_timeout_seconds"`
	ReadinessPort     int                   `json:"readiness_port,omitempty"`
	ReadinessAddress  string                `json:"readiness_address,omitempty"`
}

type VMRestoreDiskEffect struct {
	Target      string `json:"target,omitempty"`
	Format      string `json:"format,omitempty"`
	Destination string `json:"destination"`
	Replace     bool   `json:"replace"`
}

type VMUnsupportedDisk struct {
	Target     string `json:"target"`
	SourceType string `json:"source_type"`
}

type VMRestoreDomainEffect struct {
	State             string   `json:"state,omitempty"`
	Replace           bool     `json:"replace"`
	Stop              bool     `json:"stop"`
	RemoveManagedSave bool     `json:"remove_managed_save"`
	MayRemoveNVRAM    []string `json:"may_remove_nvram,omitempty"`
}

type VMReadinessResult struct {
	Startup           string `json:"startup"`
	Running           string `json:"running"`
	GuestCheck        string `json:"guest_check,omitempty"`
	GuestOutcome      string `json:"guest_outcome,omitempty"`
	TCPPort           int    `json:"tcp_port,omitempty"`
	CleanupStop       string `json:"cleanup_stop"`
	ApplicationHealth string `json:"application_health"`
}

type vmReadinessKey struct{}

// WithVMReadinessResult records measured outcomes in the tracked restore item.
func WithVMReadinessResult(ctx context.Context, result *VMReadinessResult) context.Context {
	*result = VMReadinessResult{
		Startup: "not_attempted", Running: "not_attempted", CleanupStop: "not_attempted", ApplicationHealth: "unchecked",
	}
	return context.WithValue(ctx, vmReadinessKey{}, result)
}

type vmExpectationKey struct{}

type vmRestoreExpectation struct {
	Key    []byte
	Digest string
}

// WithVMRestoreExpectation binds VMHandler.Restore to the reviewed operation.
func WithVMRestoreExpectation(ctx context.Context, key []byte, digest string) context.Context {
	return context.WithValue(ctx, vmExpectationKey{}, vmRestoreExpectation{key, digest})
}

// InspectVMBackup validates the saved inventory against recorded recovered
// object names. It does not contact libvirt or inspect the live destinations.
func InspectVMBackup(xmlData, metadata []byte, files []string) (VMBackupEvidence, error) {
	evidence, _, _, err := inspectVMBackup(xmlData, metadata, files)
	return evidence, err
}

func inspectVMBackup(xmlData, metadata []byte, files []string) (VMBackupEvidence, vmBackupMetadata, domainDiskInventory, error) {
	var evidence VMBackupEvidence
	var identity struct {
		XMLName xml.Name `xml:"domain"`
		Names   []string `xml:"name"`
		UUIDs   []string `xml:"uuid"`
		OS      struct {
			NVRAMs []struct {
				Source *struct{} `xml:"source"`
			} `xml:"nvram"`
		} `xml:"os"`
	}
	if err := xml.Unmarshal(xmlData, &identity); err != nil || len(identity.Names) != 1 || len(identity.UUIDs) != 1 || identity.Names[0] == "" {
		return evidence, vmBackupMetadata{}, domainDiskInventory{}, ErrVMBackupEvidenceUnknown
	}
	evidence.Name, evidence.UUID = identity.Names[0], strings.ToLower(identity.UUIDs[0])
	if _, err := vmUUIDBytes(evidence.UUID); err != nil {
		return evidence, vmBackupMetadata{}, domainDiskInventory{}, err
	}
	meta, err := parseVMRestoreMetadata(metadata)
	if err != nil {
		return evidence, vmBackupMetadata{}, domainDiskInventory{}, ErrVMBackupEvidenceUnknown
	}
	inventory, err := parseDomainDiskInventory(string(xmlData))
	if err != nil {
		return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
	}
	// The existing VM backup only saves file-based NVRAM specified as text.
	if len(identity.OS.NVRAMs) > 1 || (len(identity.OS.NVRAMs) == 1 &&
		(identity.OS.NVRAMs[0].Source != nil || inventory.NVRAMPath == "")) {
		return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
	}
	evidence.BackupType, evidence.Checkpoint, evidence.ParentCheckpoint = meta.BackupType, meta.Checkpoint, meta.ParentCheckpoint
	if len(meta.Disks) != len(inventory.Disks) {
		return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
	}
	usedFiles := map[string]bool{"domain.xml": true, vmMetadataFileName: true}
	if inventory.NVRAMPath != "" {
		evidence.NVRAMFile = filepath.Base(inventory.NVRAMPath)
		if usedFiles[evidence.NVRAMFile] || !validVMBackupFilename(evidence.NVRAMFile) {
			return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
		}
		if !slices.Contains(files, evidence.NVRAMFile) {
			return evidence, meta, inventory, ErrVMBackupArtifactMissing
		}
		usedFiles[evidence.NVRAMFile] = true
	}
	for _, disk := range inventory.Disks {
		index := slices.IndexFunc(meta.Disks, func(record vmDiskMeta) bool { return record.Target == disk.Target })
		if !validVMTargetName(disk.Target) || index < 0 {
			return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
		}
		record := meta.Disks[index]
		if !validVMBackupFilename(record.BackupFile) || usedFiles[record.BackupFile] || record.Format != disk.Format ||
			(record.Format != "raw" && record.Format != "qcow2") {
			return evidence, meta, inventory, ErrVMBackupEvidenceUnknown
		}
		if !slices.Contains(files, record.BackupFile) {
			return evidence, meta, inventory, ErrVMBackupArtifactMissing
		}
		usedFiles[record.BackupFile] = true
		evidence.Disks = append(evidence.Disks, VMBackupDisk(record))
	}
	return evidence, meta, inventory, nil
}

func validVMBackupFilename(name string) bool {
	normalized, err := normalizeRestoreComponent(name)
	return err == nil && normalized == name
}

func vmUUIDBytes(uuid string) ([16]byte, error) {
	var raw [16]byte
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return raw, ErrVMBackupEvidenceUnknown
	}
	if n, err := hex.Decode(raw[:], []byte(strings.ReplaceAll(uuid, "-", ""))); err != nil || n != len(raw) {
		return raw, ErrVMBackupEvidenceUnknown
	}
	return raw, nil
}

type vmApprovedPlan struct {
	Plan              *vmRestorePlan
	Meta              vmBackupMetadata
	Effects           VMRestoreEffects
	UnresolvedTargets []string
}

// buildApprovedVMPlan reuses the manual restore path policies. It maps disks
// by their recorded target and filename so a changed XML disk order or a
// different delta extension cannot select the wrong artifact.
func buildApprovedVMPlan(name, destination string, xmlData, metadata []byte, files []string) (vmApprovedPlan, error) {
	evidence, meta, inventory, err := inspectVMBackup(xmlData, metadata, files)
	if err != nil {
		return vmApprovedPlan{}, err
	}
	if evidence.Name != name {
		return vmApprovedPlan{}, ErrVMRestoreIdentityChanged
	}
	if destination != "" {
		if _, err := safepath.CheckCustomRestoreDestination(destination); err != nil {
			return vmApprovedPlan{}, ErrVMRestoreTargetDisallowed
		}
		destination, err = normalizeRestorePath(destination)
		if err != nil {
			return vmApprovedPlan{}, ErrVMRestoreTargetDisallowed
		}
	}
	plan, err := buildVMRestorePlan(xmlData, "")
	if err != nil {
		return vmApprovedPlan{}, ErrVMBackupEvidenceUnknown
	}
	effects := VMRestoreEffects{
		Name: evidence.Name, UUID: evidence.UUID, Destination: destination, Disks: make([]VMRestoreDiskEffect, 0, len(evidence.Disks)),
		StartAfterRestore: meta.startAfterRestore(), ReadinessMode: meta.RestoreVerify.Mode,
		ReadinessTimeout: meta.RestoreVerify.TimeoutSeconds, ReadinessPort: meta.RestoreVerify.TCPPort,
	}
	if meta.RestoreVerify.Mode == vmRestoreVerifyModeTCP {
		effects.ReadinessAddress = "automatic"
		if meta.RestoreVerify.TCPHost != "" {
			effects.ReadinessAddress = "explicit"
		}
	}
	for _, disk := range inventory.Skipped {
		target, sourceType := disk.Target, disk.SourceType
		if !validVMTargetName(target) {
			target = "unknown"
		}
		switch sourceType {
		case "block", "network", "volume", "dir":
		default:
			sourceType = "unknown"
		}
		effects.UnsupportedDisks = append(effects.UnsupportedDisks, VMUnsupportedDisk{target, sourceType})
	}

	var sourcePaths []string
	for i := range plan.Disks {
		disk := &plan.Disks[i]
		disk.BackupFile = evidence.Disks[i].BackupFile
		if destination != "" {
			disk.TargetPath = filepath.Join(destination, filepath.Base(disk.TargetPath))
		}
		sourcePaths = append(sourcePaths, disk.TargetPath)
	}
	if destination != "" && plan.NVRAMTargetPath != "" {
		plan.NVRAMTargetPath = filepath.Join(destination, evidence.NVRAMFile)
	}
	if plan.NVRAMTargetPath != "" {
		sourcePaths = append(sourcePaths, plan.NVRAMTargetPath)
	}
	if err := normalizeVMRestorePlan(plan); err != nil {
		return vmApprovedPlan{}, ErrVMRestoreTargetDisallowed
	}

	paths := make(map[string]bool, len(plan.Disks)+1)
	replacements := make(map[string]string, len(plan.Disks)+1)
	for i, disk := range plan.Disks {
		if paths[disk.TargetPath] {
			return vmApprovedPlan{}, ErrVMRestoreTargetCollision
		}
		paths[disk.TargetPath] = true
		replacements[inventory.Disks[i].Path] = disk.TargetPath
		effects.Disks = append(effects.Disks, VMRestoreDiskEffect{
			Target: evidence.Disks[i].Target, Format: evidence.Disks[i].Format, Destination: disk.TargetPath,
		})
	}
	if plan.NVRAMTargetPath != "" {
		if paths[plan.NVRAMTargetPath] {
			return vmApprovedPlan{}, ErrVMRestoreTargetCollision
		}
		replacements[inventory.NVRAMPath] = plan.NVRAMTargetPath
		effects.NVRAM = &VMRestoreDiskEffect{Destination: plan.NVRAMTargetPath}
	}
	plan.DomainXML, err = rewriteApprovedVMXML(plan.DomainXML, replacements)
	if err != nil {
		return vmApprovedPlan{}, err
	}
	return vmApprovedPlan{Plan: plan, Meta: meta, Effects: effects, UnresolvedTargets: sourcePaths}, nil
}

func validVMTargetName(target string) bool {
	if len(target) == 0 || len(target) > 64 {
		return false
	}
	for _, c := range target {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

var vmXMLAttribute = regexp.MustCompile(`([^\s=/<>]+)\s*=\s*("[^"]*"|'[^']*')`)

// Patch only storage locations, preserving the saved XML's namespaces,
// unrelated attributes, device configuration and text.
func rewriteApprovedVMXML(document string, replacements map[string]string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(document))
	var output strings.Builder
	var stack []string
	var disk bool
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if len(replacements) != 0 {
				return "", ErrVMBackupEvidenceUnknown
			}
			return output.String(), nil
		}
		if err != nil {
			return "", ErrVMBackupEvidenceUnknown
		}
		raw := document[start:decoder.InputOffset()]
		switch value := token.(type) {
		case xml.StartElement:
			stack = append(stack, value.Name.Local)
			switch {
			case slices.Equal(stack, []string{"domain", "devices", "disk"}):
				disk = false
				for _, attr := range value.Attr {
					disk = disk || (attr.Name.Space == "" && attr.Name.Local == "device" && attr.Value == "disk")
				}
			case disk && slices.Equal(stack, []string{"domain", "devices", "disk", "source"}):
				for _, attr := range value.Attr {
					if attr.Name.Space != "" || attr.Name.Local != "file" {
						continue
					}
					path, ok := replacements[attr.Value]
					if !ok {
						return "", ErrVMBackupEvidenceUnknown
					}
					delete(replacements, attr.Value)
					if path == attr.Value {
						break
					}
					for _, match := range vmXMLAttribute.FindAllStringSubmatchIndex(raw, -1) {
						if raw[match[2]:match[3]] == "file" {
							raw = raw[:match[4]+1] + escapeVMXML(path) + raw[match[5]-1:]
							break
						}
					}
				}
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if slices.Equal(stack, []string{"domain", "os", "nvram"}) {
				original := strings.TrimSpace(string(value))
				if path, ok := replacements[original]; ok {
					delete(replacements, original)
					if path != original {
						raw = escapeVMXML(path)
					}
				}
			}
		}
		output.WriteString(raw)
	}
}

func escapeVMXML(s string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(s))
	return escaped.String()
}

var _ = buildApprovedVMPlan
