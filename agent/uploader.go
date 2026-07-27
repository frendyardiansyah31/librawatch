package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// downloadFile fetches a file from the server's /api/file/:filename endpoint
// and saves it to the system temp directory. Returns the local file path.
// If expectedChecksum is non-empty, the downloaded bytes' sha256 must match
// it or the file is removed and an error is returned — the server computes
// this from the same upload the agent is about to execute, so a mismatch
// means the file changed (or was corrupted) in transit.
func downloadFile(filename, expectedChecksum string) (string, error) {
	if serverBaseURL == "" {
		return "", fmt.Errorf("server base URL not set")
	}
	url := serverBaseURL + "/api/file/" + filepath.Base(filename)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %d", resp.StatusCode)
	}

	localPath := filepath.Join(os.TempDir(), filepath.Base(filename))
	f, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hasher), resp.Body)
	f.Close()
	if copyErr != nil {
		return "", fmt.Errorf("write file: %w", copyErr)
	}

	if expectedChecksum != "" {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != expectedChecksum {
			os.Remove(localPath)
			return "", fmt.Errorf("checksum mismatch: expected %s got %s", expectedChecksum, got)
		}
	}

	logMsg("INFO", "Downloaded %s → %s", filename, localPath)
	return localPath, nil
}
