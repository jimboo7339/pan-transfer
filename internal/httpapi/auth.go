package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/secret"
)

// Session lifetime and cookie policy.
const (
	sessionTTL = 30 * 24 * time.Hour

	// cookieName is the HttpOnly fallback for browsers. The SPA keeps its own
	// copy of the token in localStorage and sends it via Authorization, because
	// a pure-cookie design cannot be CSRF-proof without an extra token round trip.
	cookieName = "pt_session"

	// loginMaxFailures / loginLockWindow bound password guessing. Four digits of
	// PIN is a realistic choice for a home NAS panel, so brute force must be
	// rate-limited rather than merely "slow".
	loginMaxFailures = 5
	loginLockWindow  = 5 * time.Minute
)

// signSession issues "<expiry>.<hmac>" where the MAC covers the session version
// and expiry. Bumping the version (password change / explicit revoke) invalidates
// every outstanding token, which is what makes logout and credential rotation
// actually take effect.
func (s *Server) signSession(expiry int64) string {
	mac := hmac.New(sha256.New, []byte(s.secretKey()))
	fmt.Fprintf(mac, "session:%d:%s", expiry, s.sessionVersion(sessionCtx))
	return fmt.Sprintf("%d.%s", expiry, hex.EncodeToString(mac.Sum(nil)))
}

// sessionCtx is the context used for signing outside a request (login/logout).
var sessionCtx = context.Background()

func (s *Server) ctxForSigning() context.Context { return sessionCtx }

// sessionVersion reads the generation counter mixed into every token signature.
// Cached by the Server to avoid a DB hit per request; explicitly refreshed by
// bumpSessionVersion after a password change or logout-all.
func (s *Server) sessionVersion(ctx context.Context) string {
	s.versionMu.RLock()
	v := s.sessionVer
	s.versionMu.RUnlock()
	if v != "" {
		return v
	}
	v, _ = s.currentStore().GetSetting(ctx, "session_version")
	if v == "" {
		v = "1"
		_ = s.currentStore().SetSetting(ctx, "session_version", v)
	}
	s.versionMu.Lock()
	s.sessionVer = v
	s.versionMu.Unlock()
	return v
}

// bumpSessionVersion invalidates every issued token (password change, revoke-all).
func (s *Server) bumpSessionVersion(ctx context.Context) {
	next := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := s.currentStore().SetSetting(ctx, "session_version", next); err != nil {
		s.deps.Log.Warn("递增会话版本失败", "err", err)
		return
	}
	s.versionMu.Lock()
	s.sessionVer = next
	s.versionMu.Unlock()
}

// verifySession validates signature, expiry and revocation generation.
func (s *Server) verifySession(ctx context.Context, token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	var expiry int64
	if _, err := fmt.Sscanf(parts[0], "%d", &expiry); err != nil {
		return false
	}
	if time.Now().Unix() > expiry {
		return false
	}
	return hmac.Equal([]byte(s.signSession(expiry)), []byte(token))
}

// checkAuth authenticates a request.
//
// Note: the previous version also accepted ?token=..., which leaked session
// tokens into reverse-proxy access logs, shell history and Referer headers. It
// is deliberately gone — the SPA sends Authorization: Bearer, and the browser
// fallback uses the HttpOnly cookie.
func (s *Server) checkAuth(r *http.Request) error {
	ctx := r.Context()
	if !s.authEnabled(ctx) {
		return nil
	}
	if !s.originAllowed(r) {
		return domain.Errorf(domain.CodePermissionDenied, "跨站请求被拒绝")
	}
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	} else if c, err := r.Cookie(cookieName); err == nil {
		token = c.Value
	}
	if token != "" && s.verifySession(ctx, token) {
		return nil
	}
	return domain.Errorf(domain.CodeAuthExpired, "未登录或会话已过期")
}

// originAllowed rejects cross-site browser requests.
//
// A browser attaches the HttpOnly cookie to every same-origin request, so without
// this check any page the user visits could POST /api/tasks/{id}/run and burn
// their account's rate limit. Requests with no Origin header (curl, server-side
// callers, same-origin navigations) are allowed: they cannot be CSRF.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	if origin == "http://"+host || origin == "https://"+host {
		return true
	}
	// Behind a reverse proxy the browser may see a different public origin than
	// the Host the app received. Honour a configured allowlist in that case.
	if extra := strings.TrimSpace(s.allowedOrigins()); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if strings.TrimSpace(o) == origin {
				return true
			}
		}
	}
	s.deps.Log.Warn("拒绝跨站请求", "origin", origin, "host", host, "path", r.URL.Path)
	return false
}

// allowedOrigins returns the optional extra-origin allowlist.
func (s *Server) allowedOrigins() string { return s.deps.AllowedOrigins }

// setSessionCookie writes the browser session cookie.
//
// HttpOnly keeps it away from any XSS payload; SameSite=Strict closes the CSRF
// window even if originAllowed is ever loosened; Secure is enabled whenever the
// request arrived over TLS, or when PAN_TRANSFER_SECURE_COOKIE=1 forces it on
// (correct for a TLS-terminating reverse proxy).
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := strings.EqualFold(strings.TrimSpace(s.deps.ForceSecureCookie), "1") ||
		r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ---------- login rate limiting ----------

type loginAttempt struct {
	count    int
	firstAt  time.Time
	lockedTo time.Time
}

// loginLimiter throttles password guesses per client address.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string]*loginAttempt{}}
}

// allow reports whether a login attempt may proceed.
func (l *loginLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok {
		return true
	}
	if now.Before(a.lockedTo) {
		return false
	}
	if now.Sub(a.firstAt) > loginLockWindow {
		delete(l.attempts, key)
	}
	return true
}

// recordFailure counts a failed attempt and locks the key when it exceeds the
// threshold within the window.
func (l *loginLimiter) recordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok || now.Sub(a.firstAt) > loginLockWindow {
		a = &loginAttempt{firstAt: now}
		l.attempts[key] = a
	}
	a.count++
	if a.count >= loginMaxFailures {
		a.lockedTo = now.Add(loginLockWindow)
		a.count = 0
		a.firstAt = now
	}
}

// reset clears the counter after a successful login.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// clientKey identifies a login caller. Proxy-aware: the immediate peer is
// meaningless behind a reverse proxy.
func clientKey(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if rip := strings.TrimSpace(r.Header.Get("X-Real-Ip")); rip != "" {
		return rip
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// ---------- password hashing ----------

// hashPassword hashes a new password with Argon2id.
func (s *Server) hashPassword(pwd string) string {
	h, err := secret.HashPassword(pwd)
	if err != nil {
		// Argon2id needs a CSPRNG for the salt; failing closed is correct.
		s.deps.Log.Error("密码哈希失败", "err", err)
		return ""
	}
	return h
}

// verifyPassword checks a candidate password against a stored hash, accepting the
// legacy unstretched SHA-256 format so existing installs can still log in.
func verifyPassword(stored, candidate string) bool {
	return secret.VerifyPassword(stored, candidate)
}
