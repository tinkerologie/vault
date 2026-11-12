package db

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// AssistantRevocations denies revoked credentials even if the database rolls
// back to a snapshot taken before revocation.
type AssistantRevocations struct {
	mu      sync.RWMutex
	path    string
	revoked map[string]struct{}
}

func LoadAssistantRevocations(dir string) *AssistantRevocations {
	l := &AssistantRevocations{}
	if dir == "" {
		return l
	}
	path, err := filepath.Abs(filepath.Join(dir, "assistant-keys.revoked"))
	if err != nil {
		return l
	}
	l.path = path
	file, err := openRegular(l.path)
	if errors.Is(err, os.ErrNotExist) {
		l.revoked = make(map[string]struct{})
		return l
	}
	if err != nil {
		return l
	}
	defer file.Close()

	revoked := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		digest := scanner.Text()
		if !validAssistantDigest(digest) {
			return l
		}
		revoked[digest] = struct{}{}
	}
	// Vault creates the file on the first revocation. An empty existing
	// file can only come from external modification or lost data.
	if scanner.Err() == nil && len(revoked) > 0 {
		l.revoked = revoked
	}
	return l
}

func validAssistantDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func (l *AssistantRevocations) Ready() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.revoked != nil
}

func (l *AssistantRevocations) Permits(digest string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, revoked := l.revoked[digest]
	return l.revoked != nil && !revoked
}

func (l *AssistantRevocations) Revoke(digest string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.revoked == nil || !validAssistantDigest(digest) {
		return errors.New("assistant key revocation ledger unavailable")
	}
	if _, exists := l.revoked[digest]; exists {
		return nil
	}

	lines := make([]string, 0, len(l.revoked)+1)
	for entry := range l.revoked {
		lines = append(lines, entry)
	}
	lines = append(lines, digest)
	sort.Strings(lines)
	if err := writeFileAtomic(l.path, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return err
	}
	l.revoked[digest] = struct{}{}
	return nil
}
