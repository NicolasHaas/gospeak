package client

import (
	"crypto/sha256"
	"fmt"
	"os"
)

func pkiHistoryPath(addr string) (string, error) {
	return configFilePath(fmt.Sprintf("pki-history-%x", sha256.Sum256([]byte(addr))))
}

// Each address has an independent monotonic marker, so concurrent clients
// cannot overwrite another server's history. No legacy trust is inferred.
func pkiHistory(addr string, record bool) (bool, error) {
	path, err := pkiHistoryPath(addr)
	if err != nil {
		return false, err
	}
	data, err := readPrivateFile(path, true)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read PKI trust history: %w", err)
	}
	known := err == nil
	if known && string(data) != addr {
		return false, fmt.Errorf("invalid PKI trust history")
	}
	if record && !known {
		if err := writePrivateFile(path, []byte(addr)); err != nil {
			return false, fmt.Errorf("save PKI trust history: %w", err)
		}
	}
	return known, nil
}
