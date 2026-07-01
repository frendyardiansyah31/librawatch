package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter builds a minimal Gin router with auth middleware protecting GET /protected,
// and exposes POST /api/login and POST /api/logout as the real server does.
func newTestRouter(auth *AuthManager) *gin.Engine {
	r := gin.New()
	r.POST("/api/login", handleLogin(auth))
	protected := r.Group("/api", auth.Middleware())
	protected.POST("/logout", handleLogout(auth))
	protected.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

// ── AuthManager.Login ──────────────────────────────────────────────────────

// Positive: correct credentials return a non-empty token and ok=true.
func TestLogin_ValidCredentials_ReturnsToken(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")

	// Act
	token, ok := auth.Login("admin", "secret")

	// Assert
	if !ok {
		t.Fatal("expected ok=true for valid credentials, got false")
	}
	if token == "" {
		t.Fatal("expected non-empty token, got empty string")
	}
}

// Negative: wrong password returns ok=false and empty token.
func TestLogin_WrongPassword_ReturnsFalse(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")

	// Act
	token, ok := auth.Login("admin", "wrongpass")

	// Assert
	if ok {
		t.Fatal("expected ok=false for wrong password, got true")
	}
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}

// Negative: wrong username returns ok=false and empty token.
func TestLogin_WrongUsername_ReturnsFalse(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")

	// Act
	token, ok := auth.Login("hacker", "secret")

	// Assert
	if ok {
		t.Fatal("expected ok=false for wrong username, got true")
	}
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}

// ── AuthManager.Valid ──────────────────────────────────────────────────────

// Positive: token obtained from Login() is immediately valid.
func TestValid_TokenAfterLogin(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	token, _ := auth.Login("admin", "secret")

	// Act
	valid := auth.Valid(token)

	// Assert
	if !valid {
		t.Fatal("expected Valid()=true for freshly issued token")
	}
}

// Negative: a random string is not a valid session token.
func TestValid_NonExistentToken_ReturnsFalse(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")

	// Act
	valid := auth.Valid("completely-made-up-token")

	// Assert
	if valid {
		t.Fatal("expected Valid()=false for unknown token, got true")
	}
}

// Negative: empty string is never valid.
func TestValid_EmptyToken_ReturnsFalse(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")

	// Act
	valid := auth.Valid("")

	// Assert
	if valid {
		t.Fatal("expected Valid()=false for empty token, got true")
	}
}

// ── AuthManager.Logout ─────────────────────────────────────────────────────

// Positive: after Logout(), the token is no longer valid.
func TestLogout_TokenBecomesInvalid(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	token, _ := auth.Login("admin", "secret")

	// Act
	auth.Logout(token)

	// Assert
	if auth.Valid(token) {
		t.Fatal("expected Valid()=false after Logout(), got true")
	}
}

// ── HTTP login handler ─────────────────────────────────────────────────────

// Positive: POST /api/login with correct credentials returns 200 and a token.
func TestLoginHandler_ValidCredentials_Returns200(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	r := newTestRouter(auth)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if resp["token"] == "" {
		t.Fatalf("expected non-empty token in response, got: %v", resp)
	}
}

// Negative: POST /api/login with wrong credentials returns 401.
func TestLoginHandler_WrongCredentials_Returns401(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	r := newTestRouter(auth)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "bad"})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// Negative: POST /api/login with malformed JSON returns 400.
func TestLoginHandler_MissingBody_Returns400(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── Auth middleware ────────────────────────────────────────────────────────

// Positive: a valid Bearer token passes the middleware and reaches the handler.
func TestMiddleware_ValidToken_Allows(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	token, _ := auth.Login("admin", "secret")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d", w.Code)
	}
}

// Negative: request with no Authorization header is rejected with 401.
func TestMiddleware_MissingToken_Returns401(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d", w.Code)
	}
}

// Negative: request with an invalid Bearer token is rejected with 401.
func TestMiddleware_InvalidToken_Returns401(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-xyz")
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid token, got %d", w.Code)
	}
}

// Positive: when auth is disabled (username=""), all requests pass through.
func TestMiddleware_AuthDisabled_AllowsAll(t *testing.T) {
	// Arrange — empty username disables auth
	auth := NewAuthManager("", "")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	// deliberately no Authorization header
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when auth is disabled, got %d", w.Code)
	}
}

// Positive: after logout, accessing a protected route with the old token returns 401.
func TestMiddleware_AfterLogout_TokenRejected(t *testing.T) {
	// Arrange
	auth := NewAuthManager("admin", "secret")
	token, _ := auth.Login("admin", "secret")
	auth.Logout(token)
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	// Act
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for logged-out token, got %d", w.Code)
	}
}
