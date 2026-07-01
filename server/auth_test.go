package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// fastAuth creates an AuthManager with bcrypt.MinCost (cost=4) for fast tests.
func fastAuth(username, password string) *AuthManager {
	return newAuthManagerWithCost(username, password, bcrypt.MinCost)
}

func newTestRouter(auth *AuthManager) *gin.Engine {
	return newTestRouterFull(auth, nil, nil)
}

func newTestRouterWithDB(auth *AuthManager, db *DB) *gin.Engine {
	return newTestRouterFull(auth, db, nil)
}

func newTestRouterFull(auth *AuthManager, db *DB, limiter *LoginRateLimiter) *gin.Engine {
	r := gin.New()
	r.POST("/api/login", handleLogin(auth, db, limiter))
	protected := r.Group("/api", auth.Middleware())
	protected.POST("/logout", handleLogout(auth, db))
	protected.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

// ── AuthManager.Login ──────────────────────────────────────────────────────

// Positive: correct credentials return a non-empty token and ok=true.
func TestLogin_ValidCredentials_ReturnsToken(t *testing.T) {
	// Arrange
	auth := fastAuth("admin", "secret")

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
	auth := fastAuth("admin", "secret")

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
	auth := fastAuth("admin", "secret")

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

// Positive: pre-hashed bcrypt password in config works correctly.
func TestLogin_PreHashedPassword_Works(t *testing.T) {
	// Arrange — simulate admin storing a pre-computed hash in config
	hash, _ := bcrypt.GenerateFromPassword([]byte("mypassword"), bcrypt.MinCost)
	auth := fastAuth("admin", string(hash))

	// Act
	_, ok := auth.Login("admin", "mypassword")

	// Assert
	if !ok {
		t.Fatal("expected login to succeed with pre-hashed password")
	}
}

// ── AuthManager.Valid ──────────────────────────────────────────────────────

// Positive: token obtained from Login() is immediately valid.
func TestValid_TokenAfterLogin(t *testing.T) {
	// Arrange
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")

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
	auth := fastAuth("admin", "secret")

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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("admin", "secret")
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
	auth := fastAuth("", "")
	r := newTestRouter(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
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
	auth := fastAuth("admin", "secret")
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

// ── LoginRateLimiter ──────────────────────────────────────────────────────

// Positive: first N attempts within limit are allowed.
func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	// Arrange
	l := NewLoginRateLimiter(5, time.Minute)

	// Act + Assert — first 4 failures still allowed
	for i := 0; i < 4; i++ {
		l.RecordFailure("1.2.3.4")
		if !l.Allow("1.2.3.4") {
			t.Fatalf("expected Allow=true after %d failures, got false", i+1)
		}
	}
}

// Negative: 5th failure causes Allow to return false (429).
func TestRateLimiter_BlocksAtLimit(t *testing.T) {
	// Arrange
	l := NewLoginRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}

	// Act
	allowed := l.Allow("1.2.3.4")

	// Assert
	if allowed {
		t.Fatal("expected Allow=false after 5 failures, got true")
	}
}

// Positive: Reset clears failures so the IP is allowed again.
func TestRateLimiter_ResetAllowsAgain(t *testing.T) {
	// Arrange
	l := NewLoginRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}

	// Act
	l.Reset("1.2.3.4")

	// Assert
	if !l.Allow("1.2.3.4") {
		t.Fatal("expected Allow=true after Reset(), got false")
	}
}

// Positive: failures from different IPs are tracked independently.
func TestRateLimiter_IndependentPerIP(t *testing.T) {
	// Arrange
	l := NewLoginRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.1.1.1")
	}

	// Act + Assert — different IP unaffected
	if !l.Allow("2.2.2.2") {
		t.Fatal("expected Allow=true for clean IP, got false")
	}
}

// Negative: rate limiter returns 429 via HTTP handler after too many failures.
func TestLoginHandler_RateLimited_Returns429(t *testing.T) {
	// Arrange
	auth := fastAuth("admin", "secret")
	limiter := NewLoginRateLimiter(3, time.Minute)
	r := newTestRouterFull(auth, nil, limiter)

	doFail := func() int {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Act — exceed limit
	for i := 0; i < 3; i++ {
		doFail()
	}

	// Assert — next attempt blocked
	if code := doFail(); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after rate limit exceeded, got %d", code)
	}
}

// Positive: successful login resets rate limit for that IP.
func TestLoginHandler_SuccessResetsRateLimit(t *testing.T) {
	// Arrange
	auth := fastAuth("admin", "secret")
	limiter := NewLoginRateLimiter(3, time.Minute)
	r := newTestRouterFull(auth, nil, limiter)

	// Fail twice
	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	// Succeed — should reset counter
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid login, got %d", w.Code)
	}

	// Act — fail again after reset; should not be blocked yet
	body, _ = json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	req = httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Assert — 401 (not blocked), counter reset
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (not blocked) after reset, got %d", w.Code)
	}
}
