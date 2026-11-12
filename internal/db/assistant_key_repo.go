package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AssistantKey is the metadata visible to an operator.
type AssistantKey struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	RestoreRequest bool      `json:"restore_request"`
	RestoreGrant   string    `json:"-"`
}

func (d *DB) CreateAssistantKey(ctx context.Context, name, digest string) (AssistantKey, error) {
	created := time.Now().UTC().Truncate(time.Second)
	result, err := d.ExecContext(ctx, `INSERT INTO assistant_keys (name, key_digest, created_at) VALUES (?, ?, ?)`, name, digest, created)
	if err != nil {
		return AssistantKey{}, err
	}
	id, err := result.LastInsertId()
	return AssistantKey{ID: id, Name: name, CreatedAt: created}, err
}

func (d *DB) ListAssistantKeys(ctx context.Context) ([]AssistantKey, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, created_at, restore_request_grant FROM assistant_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]AssistantKey, 0)
	for rows.Next() {
		var key AssistantKey
		if err := rows.Scan(&key.ID, &key.Name, &key.CreatedAt, &key.RestoreGrant); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (d *DB) FindAssistantKey(ctx context.Context, digest string) (AssistantKey, error) {
	return scanAssistantKey(d.QueryRowContext(ctx, `SELECT id, name, created_at, restore_request_grant FROM assistant_keys WHERE key_digest = ?`, digest))
}

func (d *DB) SetAssistantRestoreGrant(ctx context.Context, id int64, grant string) (AssistantKey, error) {
	return scanAssistantKey(d.QueryRowContext(ctx, `UPDATE assistant_keys SET restore_request_grant = ? WHERE id = ?
		RETURNING id, name, created_at, restore_request_grant`, grant, id))
}

func scanAssistantKey(row *sql.Row) (AssistantKey, error) {
	var key AssistantKey
	err := row.Scan(&key.ID, &key.Name, &key.CreatedAt, &key.RestoreGrant)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantKey{}, ErrNotFound
	}
	return key, err
}

func (d *DB) AssistantKeyDigest(ctx context.Context, id int64) (string, error) {
	var digest string
	err := d.QueryRowContext(ctx, `SELECT key_digest FROM assistant_keys WHERE id = ?`, id).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return digest, err
}

func (d *DB) DeleteAssistantKey(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM assistant_keys WHERE id = ?`, id)
	return err
}
