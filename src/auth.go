package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pbkdf2Iterations = 120000

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 10000 || iter > 2000000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

type SessionStore struct {
	mu       sync.Mutex
	data     map[string]time.Time
	tokenTTL time.Duration
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{data: map[string]time.Time{}, tokenTTL: ttl}
}

func (s *SessionStore) Create() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.data[token] = time.Now().Add(s.tokenTTL)
	s.mu.Unlock()
	return token
}

func (s *SessionStore) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.data[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.data, token)
		return false
	}
	return true
}

func (s *SessionStore) Drop(token string) {
	s.mu.Lock()
	delete(s.data, token)
	s.mu.Unlock()
}

func (s *SessionStore) Cleanup() {
	s.mu.Lock()
	now := time.Now()
	for k, exp := range s.data {
		if now.After(exp) {
			delete(s.data, k)
		}
	}
	s.mu.Unlock()
}

type LoginLimiter struct {
	mu    sync.Mutex
	fails map[string]*failState
}

type failState struct {
	count int
	until time.Time
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{fails: map[string]*failState{}}
}

func (l *LoginLimiter) Blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.fails[ip]
	if !ok {
		return false, 0
	}
	if time.Now().After(st.until) {
		delete(l.fails, ip)
		return false, 0
	}
	if st.count >= 5 {
		return true, time.Until(st.until)
	}
	return false, 0
}

func (l *LoginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.fails[ip]
	if !ok {
		st = &failState{until: time.Now().Add(10 * time.Minute)}
		l.fails[ip] = st
	}
	st.count++
	st.until = time.Now().Add(10 * time.Minute)
}

func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

func (l *LoginLimiter) Cleanup() {
	l.mu.Lock()
	now := time.Now()
	for k, st := range l.fails {
		if now.After(st.until) {
			delete(l.fails, k)
		}
	}
	l.mu.Unlock()
}
