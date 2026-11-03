package cli

import (
	"errors"
	"fmt"
	"sync"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// durableHostKeyPinner pins through the current database and saves a new pin
// before the connection is accepted. In hybrid mode the working database is in
// RAM, so a pin lost to a crash would let the next connection trust any key. If
// the save fails, the pin is forgotten and the connection refused, so a later
// attempt saves it again. Calls for one address are serialized, so a concurrent
// connection cannot accept a pin still being saved; other addresses do not wait.
func durableHostKeyPinner(database func() *db.DB, save func() error) storage.HostKeyPinner {
	var locks sync.Map // address -> *sync.Mutex
	return func(address, fingerprint string) (string, error) {
		v, _ := locks.LoadOrStore(address, &sync.Mutex{})
		mu := v.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		d := database()
		before, err := d.GetSetting(db.SFTPHostKeyPinPrefix+address, "")
		if err != nil {
			return "", err
		}
		pinned, err := d.PinSFTPHostKey(address, fingerprint)
		if err != nil {
			return "", err
		}
		if before != "" {
			return pinned, nil
		}
		if err := save(); err != nil {
			err = fmt.Errorf("save the new host key pin: %w", err)
			return "", errors.Join(err, d.UnpinSFTPHostKey(address))
		}
		return pinned, nil
	}
}
