package webserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const webPasswordEntropyBytes = 24

// RandomPassword returns a 192-bit password encoded without padding so it is
// safe to copy from the terminal into a browser authentication prompt.
func RandomPassword() (string, error) {
	raw := make([]byte, webPasswordEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// persistentWebPassword returns the password associated with this database.
// The insert is conflict-safe so concurrent server starts converge on the one
// password that wins the primary-key race instead of returning different
// credentials. The database is mode 0600 because this value must remain
// recoverable so it can be printed again after a restart.
func (s *Store) persistentWebPassword(ctx context.Context) (string, error) {
	password, err := s.readWebPassword(ctx)
	if err == nil {
		return password, validatePersistentWebPassword(password)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read persisted web password: %w", err)
	}

	candidate, err := RandomPassword()
	if err != nil {
		return "", fmt.Errorf("generate web password: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO web_auth (id, password, created_at)
		VALUES (1, ?, ?) ON CONFLICT(id) DO NOTHING`, candidate, time.Now().UTC().UnixNano()); err != nil {
		return "", fmt.Errorf("persist web password: %w", err)
	}

	password, err = s.readWebPassword(ctx)
	if err != nil {
		return "", fmt.Errorf("read persisted web password after creation: %w", err)
	}
	if err := validatePersistentWebPassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func (s *Store) readWebPassword(ctx context.Context) (string, error) {
	var password string
	err := s.db.QueryRowContext(ctx, `SELECT password FROM web_auth WHERE id = 1`).Scan(&password)
	return password, err
}

func validatePersistentWebPassword(password string) error {
	raw, err := base64.RawURLEncoding.DecodeString(password)
	if err != nil || len(raw) != webPasswordEntropyBytes || base64.RawURLEncoding.EncodeToString(raw) != password {
		return fmt.Errorf("persisted web password is corrupt")
	}
	return nil
}
