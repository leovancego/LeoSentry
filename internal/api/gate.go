package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "leosentry_session"

// PasswordStore 校验并修改 Web 登录密码。
type PasswordStore interface {
	CheckPassword(ctx context.Context, password string) (bool, error)
	SetPassword(ctx context.Context, password string) error
}

type sessions struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newSessions() *sessions {
	return &sessions{until: map[string]time.Time{}}
}

func (s *sessions) issue() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	s.mu.Lock()
	s.until[token] = time.Now().Add(30 * 24 * time.Hour)
	s.mu.Unlock()
	return token, nil
}

func (s *sessions) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.until[token]
	if !ok || time.Now().After(until) {
		delete(s.until, token)
		return false
	}
	return true
}

func (s *sessions) revoke(token string) {
	s.mu.Lock()
	delete(s.until, token)
	s.mu.Unlock()
}

func tokenFrom(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// protect 要求除登录以外的 /api/ 请求带有效会话。未配置密码库时不拦截。
func protect(pass PasswordStore, sess *sessions, next http.Handler) http.Handler {
	if pass == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/login" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if sess.valid(tokenFrom(r)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "需要登录"})
	})
}

func login(pass PasswordStore, sess *sessions, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		ok, err := pass.CheckPassword(r.Context(), body.Password)
		if err != nil {
			log.Error("check password", "err", err)
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "密码不正确"})
			return
		}
		token, err := sess.issue()
		if err != nil {
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		setSessionCookie(w, token)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}
}

func logout(sess *sessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess.revoke(tokenFrom(r))
		clearSessionCookie(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}
}

func changePassword(pass PasswordStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		ok, err := pass.CheckPassword(r.Context(), body.Old)
		if err != nil {
			log.Error("check password", "err", err)
			http.Error(w, "password failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "当前密码不正确"})
			return
		}
		if err := pass.SetPassword(r.Context(), body.New); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "新密码需要 4 到 64 个字符"})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}
}
