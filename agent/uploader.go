package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// downloadFile fetches a file from the server's /api/file/:filename endpoint
// and saves it to the system temp directory. Returns the local file path.
func downloadFile(filename string) (string, error) {
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
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	logMsg("INFO", "Downloaded %s → %s", filename, localPath)
	return localPath, nil
}
