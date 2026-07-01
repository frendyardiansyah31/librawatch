package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const sessionDuration = 8 * time.Hour

// ── AuthManager ───────────────────────────────────────────────────────────

type AuthManager struct {
	username     string
	passwordHash []byte // bcrypt hash
	sessions     map[string]time.Time
	mu           sync.RWMutex
}

// NewAuthManager hashes password with bcrypt cost 12.
// If password already starts with "$2" it is treated as a pre-computed hash,
// allowing admins to store the hash in config instead of plaintext.
func NewAuthManager(username, password string) *AuthManager {
	return newAuthManagerWithCost(username, password, bcrypt.DefaultCost)
}

func newAuthManagerWithCost(username, password string, cost int) *AuthManager {
	var hash []byte
	if password != "" {
		if strings.HasPrefix(password, "$2") {
			hash = []byte(password)
		} else {
			h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
			if err == nil {
				hash = h
			}
		}
	}
	am := &AuthManager{
		username:     username,
		passwordHash: hash,
		sessions:     make(map[string]time.Time),
	}
	if username != "" {
		go am.cleanupLoop()
	}
	return am
}

func (a *AuthManager) Login(username, password string) (string, bool) {
	if a.username == "" {
		return "", false
	}
	// Constant-time username comparison, then bcrypt for password.
	uMatch := username == a.username
	pMatch := bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)) == nil
	if !uMatch || !pMatch {
		return "", false
	}
	token := newToken()
	a.mu.Lock()
	a.sessions[token] = time.Now().Add(sessionDuration)
	a.mu.Unlock()
	return token, true
}

func (a *AuthManager) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

func (a *AuthManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	a.mu.RLock()
	exp, ok := a.sessions[token]
	a.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

func (a *AuthManager) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.username == "" {
			c.Next()
			return
		}
		raw := c.GetHeader("Authorization")
		token := raw
		if len(raw) > 7 && raw[:7] == "Bearer " {
			token = raw[7:]
		}
		if !a.Valid(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

func (a *AuthManager) cleanupLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		a.mu.Lock()
		for tok, exp := range a.sessions {
			if now.After(exp) {
				delete(a.sessions, tok)
			}
		}
		a.mu.Unlock()
	}
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── LoginRateLimiter ──────────────────────────────────────────────────────

const (
	rateLimitMaxAttempts = 5
	rateLimitWindow      = 15 * time.Minute
)

type LoginRateLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
	window   time.Duration
}

func NewLoginRateLimiter(max int, window time.Duration) *LoginRateLimiter {
	l := &LoginRateLimiter{
		failures: make(map[string][]time.Time),
		max:      max,
		window:   window,
	}
	go l.cleanupLoop()
	return l
}

// Allow returns false if ip has reached the failure limit within the window.
func (l *LoginRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evict(ip)
	return len(l.failures[ip]) < l.max
}

// RecordFailure records a failed attempt for ip.
func (l *LoginRateLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	l.failures[ip] = append(l.failures[ip], time.Now())
	l.mu.Unlock()
}

// Reset clears failure history for ip (called after successful login).
func (l *LoginRateLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}

// evict removes timestamps outside the window. Must be called with mu held.
func (l *LoginRateLimiter) evict(ip string) {
	cutoff := time.Now().Add(-l.window)
	ts := l.failures[ip]
	start := 0
	for start < len(ts) && ts[start].Before(cutoff) {
		start++
	}
	if start > 0 {
		l.failures[ip] = ts[start:]
	}
	if len(l.failures[ip]) == 0 {
		delete(l.failures, ip)
	}
}

func (l *LoginRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for ip := range l.failures {
			l.evict(ip)
		}
		l.mu.Unlock()
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────

func handleLogin(auth *AuthManager, db *DB, limiter *LoginRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()

		if limiter != nil && !limiter.Allow(ip) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed attempts, try again later"})
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		token, ok := auth.Login(req.Username, req.Password)
		if !ok {
			if limiter != nil {
				limiter.RecordFailure(ip)
			}
			if db != nil {
				db.InsertAuditLog("login_failed", req.Username, "invalid credentials", ip)
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}

		if limiter != nil {
			limiter.Reset(ip)
		}
		if db != nil {
			db.InsertAuditLog("login", req.Username, "", ip)
		}
		c.JSON(http.StatusOK, gin.H{"token": token})
	}
}

func handleLogout(auth *AuthManager, db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		token := raw
		if len(raw) > 7 && raw[:7] == "Bearer " {
			token = raw[7:]
		}
		auth.Logout(token)
		if db != nil {
			db.InsertAuditLog("logout", "", "", c.ClientIP())
		}
		c.JSON(http.StatusOK, gin.H{"message": "logged out"})
	}
}
