// Package auth — пароли, токены, сессии, первичная настройка и ограничение попыток входа.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Параметры argon2id по RFC 9106 (второй рекомендуемый вариант, память 64 МиБ).
// Хранятся в самом хеше, поэтому их можно поменять без миграции: старые хеши продолжат проверяться.
type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen int
}

var defaultArgon = argonParams{memory: 64 * 1024, time: 3, threads: 2, keyLen: 32, saltLen: 16}

var ErrBadHash = errors.New("неизвестный формат хеша пароля")

// HashPassword возвращает хеш в формате PHC: $argon2id$v=19$m=65536,t=3,p=2$соль$хеш
func HashPassword(password string) (string, error) {
	return hashWith(password, defaultArgon)
}

func hashWith(password string, p argonParams) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword сравнивает пароль с хешем за постоянное время.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return false, ErrBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewToken — случайный токен из n байт в base64url без паддинга.
func NewToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand не падает на поддерживаемых ОС
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken — sha256 в hex. Так в БД хранятся токены сессий, API и агентов.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenPrefix — первые 6 символов для логов и интерфейса (AGENTS.md: токены целиком не логировать).
func TokenPrefix(token string) string {
	if len(token) <= 6 {
		return token
	}
	return token[:6]
}
