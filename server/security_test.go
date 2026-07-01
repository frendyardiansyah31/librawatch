package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// ── helpers ───────────────────────────────────────────────────────────────

func newSecurityRouter(db *DB, uploadsPath string) *gin.Engine {
	r := gin.New()
	api := r.Group("/api")
	api.POST("/upload", uploadHandler(db, uploadsPath, 10))
	api.GET("/file/:filename", downloadHandler(uploadsPath))
	api.GET("/audit", auditHandler(db))
	return r
}

// Extract the handlers to closures matching the inline lambdas in api.go,
// so we can test them in isolation without wiring the full server.
func uploadHandler(db *DB, uploadsPath string, maxMB int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if maxMB > 0 && file.Size > maxMB*1024*1024 {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds size limit"})
			return
		}
		filename := filepath.Base(file.Filename)
		if filename == "." || filename == string(filepath.Separator) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
			return
		}
		ext := lowerExt(filename)
		if !allowedUploadExts[ext] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file type not allowed (.exe .msi .bat .ps1 only)"})
			return
		}
		dest := filepath.Join(uploadsPath, filename)
		if err := c.SaveUploadedFile(file, dest); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if db != nil {
			db.InsertAuditLog("upload", filename, "", c.ClientIP())
		}
		c.JSON(http.StatusOK, gin.H{"filename": filename})
	}
}

func downloadHandler(uploadsPath string) gin.HandlerFunc {
	return func(c *gin.Context) {
		safe := filepath.Base(filepath.Clean(c.Param("filename")))
		if safe == "." || safe == ".." {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
			return
		}
		dest := filepath.Join(uploadsPath, safe)
		uploadsAbs, _ := filepath.Abs(uploadsPath)
		destAbs, _ := filepath.Abs(dest)
		if len(destAbs) <= len(uploadsAbs) || destAbs[:len(uploadsAbs)+1] != uploadsAbs+string(filepath.Separator) {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
		c.File(dest)
	}
}

func auditHandler(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		logs, err := db.GetAuditLogs(100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if logs == nil {
			logs = []AuditLog{}
		}
		c.JSON(http.StatusOK, logs)
	}
}

func lowerExt(name string) string {
	ext := filepath.Ext(name)
	result := ""
	for _, ch := range ext {
		if ch >= 'A' && ch <= 'Z' {
			result += string(ch + 32)
		} else {
			result += string(ch)
		}
	}
	return result
}

func multipartUpload(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	w.Close()
	return body, w.FormDataContentType()
}

// ── Extension whitelist — upload ──────────────────────────────────────────

// Positive: uploading an .exe file succeeds.
func TestUpload_ExeAllowed_Returns200(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	body, ct := multipartUpload(t, "installer.exe", "MZ fake exe")
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for .exe upload, got %d — %s", w.Code, w.Body)
	}
}

// Positive: uploading a .ps1 file succeeds.
func TestUpload_Ps1Allowed_Returns200(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	body, ct := multipartUpload(t, "script.ps1", "Write-Host hello")
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for .ps1 upload, got %d", w.Code)
	}
}

// Negative: uploading a .zip file is rejected with 400.
func TestUpload_ZipBlocked_Returns400(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	body, ct := multipartUpload(t, "archive.zip", "PK fake zip")
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for .zip upload, got %d", w.Code)
	}
}

// Negative: uploading a .php file is rejected with 400.
func TestUpload_PhpBlocked_Returns400(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	body, ct := multipartUpload(t, "shell.php", "<?php system($_GET['cmd']); ?>")
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for .php upload, got %d", w.Code)
	}
}

// ── Path traversal — download ─────────────────────────────────────────────

// Positive: downloading a valid file in uploads dir succeeds.
func TestDownload_ValidFile_Returns200(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tool.exe"), []byte("MZ"), 0644)
	r := newSecurityRouter(nil, dir)
	req := httptest.NewRequest(http.MethodGet, "/api/file/tool.exe", nil)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid file, got %d", w.Code)
	}
}

// Negative: path traversal attempt is blocked.
func TestDownload_PathTraversal_Returns400Or403(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	// Gin normalizes the URL path, so we test what reaches the handler param.
	// Simulate the param value that would result from URL encoding.
	req := httptest.NewRequest(http.MethodGet, "/api/file/..%2Fconfig.yaml", nil)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert — must NOT be 200
	if w.Code == http.StatusOK {
		t.Fatalf("path traversal should be blocked, got 200")
	}
}

// Negative: filename that is just dots is rejected.
func TestDownload_DotDot_Returns400(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	r := newSecurityRouter(nil, dir)
	req := httptest.NewRequest(http.MethodGet, "/api/file/..", nil)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 for '..' filename, got 200")
	}
}

// ── Audit log ─────────────────────────────────────────────────────────────

// Positive: successful login creates an audit log entry.
func TestAuditLog_LoginSuccess_CreatesEntry(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	auth := NewAuthManager("admin", "secret")
	r := newTestRouterWithDB(auth, db)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d", w.Code)
	}
	logs, err := db.GetAuditLogs(10)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected audit log entry after successful login, got none")
	}
	if logs[0].Action != "login" {
		t.Fatalf("expected action=login, got %q", logs[0].Action)
	}
}

// Negative: failed login also creates an audit log entry with action=login_failed.
func TestAuditLog_LoginFailed_CreatesEntry(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	auth := NewAuthManager("admin", "secret")
	r := newTestRouterWithDB(auth, db)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	logs, err := db.GetAuditLogs(10)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected audit log entry after failed login, got none")
	}
	if logs[0].Action != "login_failed" {
		t.Fatalf("expected action=login_failed, got %q", logs[0].Action)
	}
}

// Positive: GetAuditLogs returns entries newest-first.
func TestAuditLog_GetLogs_NewestFirst(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	db.InsertAuditLog("login", "admin", "", "127.0.0.1")
	db.InsertAuditLog("deploy", "agent-1", "type=exec", "127.0.0.1")
	db.InsertAuditLog("upload", "tool.exe", "size=1024", "127.0.0.1")

	// Act
	logs, err := db.GetAuditLogs(10)

	// Assert
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(logs))
	}
	if logs[0].Action != "upload" {
		t.Fatalf("expected newest entry first (upload), got %q", logs[0].Action)
	}
}

// Negative: GetAuditLogs with limit=0 falls back to default (100) without error.
func TestAuditLog_ZeroLimit_UsesDefault(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	db.InsertAuditLog("login", "admin", "", "127.0.0.1")

	// Act
	logs, err := db.GetAuditLogs(0)

	// Assert
	if err != nil {
		t.Fatalf("expected no error with limit=0, got %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected entries returned with default limit")
	}
}
