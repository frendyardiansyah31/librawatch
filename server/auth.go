package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const sessionDuration = 8 * time.Hour

type AuthManager struct {
	username string
	password string
	sessions map[string]time.Time
	mu       sync.RWMutex
}

func NewAuthManager(username, password string) *AuthManager {
	am := &AuthManager{
		username: username,
		password: password,
		sessions: make(map[string]time.Time),
	}
	if username != "" {
		go am.cleanupLoop()
	}
	return am
}

func (a *AuthManager) Login(username, password string) (string, bool) {
	uOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	pOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
	if !uOK || !pOK {
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

func handleLogin(auth *AuthManager) gin.HandlerFunc {
	return func(c *gin.Context) {
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
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token})
	}
}

func handleLogout(auth *AuthManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		token := raw
		if len(raw) > 7 && raw[:7] == "Bearer " {
			token = raw[7:]
		}
		auth.Logout(token)
		c.JSON(http.StatusOK, gin.H{"message": "logged out"})
	}
}
