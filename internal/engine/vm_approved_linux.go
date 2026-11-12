//go:build linux

package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/tinkerologie/vault/internal/bounded"
)

// ReviewVMRestore captures the live effects of restoring the selected
// configuration and returns a keyed expectation for the mutation boundary.
func (h *VMHandler) ReviewVMRestore(ctx context.Context, name, destination string, xmlData, metadata []byte, artifacts []string, key []byte) (VMRestoreEffects, string, error) {
	stopAbort := h.abortLibvirtOnCancel(ctx, 0)
	defer stopAbort()
	reviewed, digest, err := h.reviewVMRestore(ctx, name, destination, xmlData, metadata, artifacts, key)
	return reviewed.Effects, digest, err
}

type vmFileIdentity struct {
	Path   string
	Device uint64
	Inode  uint64
	Mode   uint32
}

type vmReviewedTarget struct {
	File   vmFileIdentity
	Parent vmFileIdentity
}

func (h *VMHandler) reviewVMRestore(ctx context.Context, name, destination string, xmlData, metadata []byte, artifacts []string, key []byte) (vmApprovedPlan, string, error) {
	if len(key) == 0 || ctx.Err() != nil {
		return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
	}
	reviewed, err := buildApprovedVMPlan(name, destination, xmlData, metadata, artifacts)
	if err != nil {
		return vmApprovedPlan{}, "", err
	}
	effects := &reviewed.Effects
	uuid, err := vmUUIDBytes(effects.UUID)
	if err != nil {
		return vmApprovedPlan{}, "", err
	}
	current, err := h.conn.DomainLookupByName(name)
	var previousXML []string
	switch {
	case err == nil:
		if current.UUID != uuid {
			return vmApprovedPlan{}, "", ErrVMRestoreIdentityChanged
		}
		state, _, err := h.conn.DomainGetState(current, 0)
		if err != nil {
			return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
		}
		saved, err := h.conn.DomainHasManagedSaveImage(current, 0)
		if err != nil {
			return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
		}
		previousXML, err = h.reviewDomainXML(current)
		if err != nil {
			return vmApprovedPlan{}, "", err
		}
		effects.Domain = VMRestoreDomainEffect{
			State: domainStateString(libvirt.DomainState(state)), Replace: true,
			Stop: !libvirtDomainIsShutOff(libvirt.DomainState(state)), RemoveManagedSave: saved != 0,
		}
	case !isLibvirtNoDomainError(err):
		return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
	}

	var identities []vmReviewedTarget
	paths := make(map[string]bool)
	// Legacy undefine removes the existing domain's NVRAM when libvirt
	// supports the flag, even if that path differs from the saved point.
	for _, doc := range previousXML {
		nvram, err := vmDomainNVRAM(doc)
		if err != nil {
			return vmApprovedPlan{}, "", err
		}
		if nvram == "" {
			continue
		}
		resolved, err := normalizeVMNVRAMTarget(nvram)
		if err != nil {
			return vmApprovedPlan{}, "", ErrVMRestoreTargetDisallowed
		}
		if !slices.Contains(effects.Domain.MayRemoveNVRAM, resolved) {
			effects.Domain.MayRemoveNVRAM = append(effects.Domain.MayRemoveNVRAM, resolved)
			identity, err := reviewVMTarget(nvram, resolved)
			if err != nil {
				return vmApprovedPlan{}, "", err
			}
			identities = append(identities, identity)
			paths[resolved] = true
		}
	}

	for i := range effects.Disks {
		disk := &effects.Disks[i]
		identity, err := reviewVMTarget(reviewed.UnresolvedTargets[i], disk.Destination)
		if err != nil {
			return vmApprovedPlan{}, "", err
		}
		identities = append(identities, identity)
		paths[disk.Destination] = true
		disk.Replace = identity.File.Inode != 0
	}
	if effects.NVRAM != nil {
		identity, err := reviewVMTarget(reviewed.UnresolvedTargets[len(effects.Disks)], effects.NVRAM.Destination)
		if err != nil {
			return vmApprovedPlan{}, "", err
		}
		identities = append(identities, identity)
		paths[effects.NVRAM.Destination] = true
		effects.NVRAM.Replace = identity.File.Inode != 0
	}

	domains, _, err := h.conn.ConnectListAllDomains(1, 0)
	if err != nil {
		return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
	}
	for _, domain := range domains {
		if ctx.Err() != nil {
			return vmApprovedPlan{}, "", ctx.Err()
		}
		if domain.Name == name {
			if !effects.Domain.Replace || domain.UUID != uuid {
				return vmApprovedPlan{}, "", ErrVMRestoreIdentityChanged
			}
			continue
		}
		if domain.UUID == uuid {
			return vmApprovedPlan{}, "", ErrVMRestoreIdentityChanged
		}
		docs, err := h.reviewDomainXML(domain)
		if err != nil {
			return vmApprovedPlan{}, "", err
		}
		for _, doc := range docs {
			if err := checkVMSharedTargets(doc, paths); err != nil {
				return vmApprovedPlan{}, "", err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return vmApprovedPlan{}, "", err
	}

	mac := hmac.New(sha256.New, key)
	err = json.NewEncoder(mac).Encode([]any{"vault-reviewed-vm-effects-v1", xmlData, metadata, effects, identities, previousXML})
	if err != nil {
		return vmApprovedPlan{}, "", ErrVMRestoreHostUnavailable
	}
	return reviewed, hex.EncodeToString(mac.Sum(nil)), nil
}

func (h *VMHandler) reviewDomainXML(domain libvirt.Domain) ([]string, error) {
	current, err := h.conn.DomainGetXMLDesc(domain, 0)
	if err != nil {
		return nil, ErrVMRestoreHostUnavailable
	}
	if domain.ID < 0 {
		return []string{current}, nil
	}
	inactive, err := h.conn.DomainGetXMLDesc(domain, libvirt.DomainXMLInactive)
	if err != nil {
		return nil, ErrVMRestoreHostUnavailable
	}
	return []string{current, inactive}, nil
}

func vmDomainNVRAM(doc string) (string, error) {
	var domain struct {
		XMLName xml.Name `xml:"domain"`
		OS      struct {
			NVRAM []struct {
				Path   string `xml:",chardata"`
				Source struct {
					File string `xml:"file,attr"`
				} `xml:"source"`
			} `xml:"nvram"`
		} `xml:"os"`
	}
	if err := xml.Unmarshal([]byte(doc), &domain); err != nil || len(domain.OS.NVRAM) > 1 {
		return "", ErrVMRestoreHostUnavailable
	}
	if len(domain.OS.NVRAM) == 0 {
		return "", nil
	}
	nvram := domain.OS.NVRAM[0]
	if nvram.Source.File != "" {
		return nvram.Source.File, nil
	}
	return strings.TrimSpace(nvram.Path), nil
}

func checkVMSharedTargets(document string, targets map[string]bool) error {
	decoder := xml.NewDecoder(strings.NewReader(document))
	nvram, err := vmDomainNVRAM(document)
	if err != nil {
		return err
	}
	if err := checkVMSharedPath(nvram, targets); err != nil {
		return err
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrVMRestoreHostUnavailable
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "source" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Space == "" && attr.Name.Local == "file" {
				if err := checkVMSharedPath(attr.Value, targets); err != nil {
					return err
				}
			}
		}
	}
}

func checkVMSharedPath(path string, targets map[string]bool) error {
	if !filepath.IsAbs(path) {
		return nil
	}
	if targets[filepath.Clean(path)] {
		return ErrVMRestoreTargetInUse
	}
	resolved, err := resolveRestorePath(path)
	if err != nil {
		return ErrVMRestoreHostUnavailable
	}
	if targets[resolved] {
		return ErrVMRestoreTargetInUse
	}
	return nil
}

func reviewVMTarget(configured, resolved string) (vmReviewedTarget, error) {
	var result vmReviewedTarget
	info, err := os.Lstat(configured)
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
			return result, ErrVMRestoreTargetDisallowed
		}
		result.File = vmFileIdentity{Path: resolved, Device: stat.Dev, Inode: stat.Ino, Mode: stat.Mode}
		// A running guest can continue writing its own disk between
		// review and approval. Bind the file identity, not its contents.
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, ErrVMRestoreHostUnavailable
	}
	for parent := filepath.Dir(resolved); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() {
				return result, ErrVMRestoreTargetDisallowed
			}
			result.Parent = vmFileIdentity{Path: parent, Device: stat.Dev, Inode: stat.Ino, Mode: stat.Mode}
			return result, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return result, ErrVMRestoreHostUnavailable
		}
	}
}

func readApprovedVMSource(dir string) (xmlData, metadata []byte, artifacts []string, err error) {
	for _, file := range []struct {
		name string
		dest *[]byte
	}{{"domain.xml", &xmlData}, {vmMetadataFileName, &metadata}} {
		body, err := os.Open(filepath.Join(dir, file.name)) // #nosec G304 -- fixed names in private Runner staging
		if err != nil {
			return nil, nil, nil, err
		}
		*file.dest, err = bounded.ReadAll(body, VMRestoreSidecarLimit)
		_ = body.Close()
		if err != nil {
			return nil, nil, nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			artifacts = append(artifacts, entry.Name())
		}
	}
	return xmlData, metadata, artifacts, nil
}
