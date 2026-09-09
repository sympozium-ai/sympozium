package controller_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Only the local operator supervisor holds the credential that can request
// tokens. The controller receives a private token file, never that credential.
type parentTokenIssuer func(context.Context) (string, time.Time, error)

func renewParentToken(ctx context.Context, path string, issue parentTokenIssuer) error {
	var expires time.Time
	for {
		if ctx.Err() != nil {
			return nil
		}
		request, cancel := context.WithTimeout(ctx, 10*time.Second)
		token, nextExpiry, err := issue(request)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		delay := 5 * time.Second
		if err == nil {
			if time.Until(nextExpiry) < 2*time.Minute {
				return errors.New("controller token renewal returned insufficient lifetime")
			}
			if err := replaceParentToken(path, token); err != nil {
				return err
			}
			expires = nextExpiry
			delay = time.Until(expires) / 2
		} else if expires.IsZero() || time.Until(expires) <= time.Minute {
			// Do not leak a provider response or continue past the last known
			// usable credential. Transient renewal failures retry within it.
			return errors.New("controller token renewal unavailable before credential safety deadline")
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func replaceParentToken(path, token string) error {
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("controller token has invalid encoding or size")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("controller token must be an existing private regular file")
	}
	// The supervisor owns this private directory. Rename publishes all bytes
	// together so client-go's token-file refresh cannot read a truncated token.
	file, err := os.CreateTemp(filepath.Dir(path), ".controller-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.WriteString(token); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
