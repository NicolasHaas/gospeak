package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

const (
	bootstrapAdminCredentialName = "bootstrap-admin.token"
	bootstrapTokenHexLength      = 64
)

// ensureAdminToken creates a retryable admin credential only when the store
// contains no tokens. The raw credential is delivered through a private file,
// never through normal process logs.
func (s *Server) ensureAdminToken(st datastore.DataProviderFactory) error {
	credentialPath := filepath.Join(serverDataDir(s.cfg), bootstrapAdminCredentialName)
	hasTokens, err := st.NonTx().HasTokens()
	if err != nil {
		return fmt.Errorf("server: check tokens: %w", err)
	}
	if hasTokens {
		state, err := st.NonTx().BootstrapTokenState()
		if err != nil {
			return fmt.Errorf("server: inspect admin bootstrap state: %w", err)
		}
		switch state {
		case datastore.BootstrapTokenPending:
			if _, err := readBootstrapCredential(credentialPath); err != nil {
				return fmt.Errorf("server: inspect admin bootstrap credential: %w", err)
			}
		case datastore.BootstrapTokenFinalized, datastore.BootstrapTokenAbsent:
			if err := removeBootstrapCredential(credentialPath); err != nil {
				return fmt.Errorf("server: retire admin bootstrap credential: %w", err)
			}
		}
		return nil
	}

	rawToken, err := loadOrCreateBootstrapCredential(credentialPath)
	if err != nil {
		return fmt.Errorf("server: prepare admin bootstrap credential: %w", err)
	}

	hash := crypto.HashToken(rawToken)
	if err := st.NonTx().CreateBootstrapToken(hash); err != nil {
		return fmt.Errorf("server: store admin bootstrap token: %w", err)
	}

	slog.Info("admin bootstrap credential created", "path", credentialPath)
	return nil
}

func (s *Server) bootstrapCredentialMatches(rawToken string) (bool, error) {
	credentialPath := filepath.Join(serverDataDir(s.cfg), bootstrapAdminCredentialName)
	want, err := readBootstrapCredential(credentialPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(rawToken), []byte(want)) == 1, nil
}

func deriveBootstrapPersonalToken(rawToken, username string) string {
	mac := hmac.New(sha256.New, []byte(rawToken))
	_, _ = mac.Write([]byte("gospeak bootstrap personal token\x00"))
	_, _ = mac.Write([]byte(username))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) tryProvisionBootstrapUser(st datastore.DataProviderFactory, tokenHash, rawToken, username string) (*model.User, string, bool, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	matches, err := s.bootstrapCredentialMatches(rawToken)
	if err != nil || !matches {
		return nil, "", false, err
	}
	rawPersonalToken := deriveBootstrapPersonalToken(rawToken, username)
	tx, err := st.Tx(context.Background())
	if err != nil {
		return nil, "", true, fmt.Errorf("start transaction: %w", err)
	}
	user, err := tx.ProvisionBootstrapUser(tokenHash, username, crypto.HashToken(rawPersonalToken), time.Now().UTC())
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return nil, "", true, fmt.Errorf("provision user: %w (rollback: %v)", err, rollbackErr)
		}
		return nil, "", true, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return nil, "", true, fmt.Errorf("commit transaction: %w", err)
	}
	return user, rawPersonalToken, true, nil
}

func (s *Server) finalizeBootstrapCredential(st datastore.DataProviderFactory, userID int64) error {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	finalized, err := st.NonTx().FinalizeBootstrapToken(userID)
	if err != nil || !finalized {
		return err
	}
	credentialPath := filepath.Join(serverDataDir(s.cfg), bootstrapAdminCredentialName)
	if err := removeBootstrapCredential(credentialPath); err != nil {
		slog.Warn("remove finalized bootstrap credential", "path", credentialPath, "err", err)
	}
	return nil
}

func removeBootstrapCredential(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func serverDataDir(cfg Config) string {
	if cfg.DataDir == "" {
		return "."
	}
	return cfg.DataDir
}

func loadOrCreateBootstrapCredential(path string) (string, error) {
	rawToken, err := readBootstrapCredential(path)
	if err == nil {
		return rawToken, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	rawToken, err = crypto.GenerateToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tempPath, err := prepareBootstrapCredentialFile(path, []byte(rawToken+"\n"))
	if err != nil {
		return "", fmt.Errorf("prepare credential file: %w", err)
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readBootstrapCredential(path)
		}
		return "", fmt.Errorf("publish credential file without overwrite: %w", err)
	}
	if err := syncBootstrapCredentialDirectory(filepath.Dir(path)); err != nil {
		return "", fmt.Errorf("sync credential directory: %w", err)
	}
	return rawToken, nil
}

func readBootstrapCredential(path string) (string, error) {
	data, err := readBootstrapCredentialFile(path)
	if err != nil {
		return "", err
	}
	rawToken := strings.TrimSuffix(string(data), "\n")
	decoded, err := hex.DecodeString(rawToken)
	if err != nil || len(decoded) != bootstrapTokenHexLength/2 {
		return "", fmt.Errorf("credential file %q contains an invalid token", path)
	}
	return rawToken, nil
}
