package auth

import (
	"sync"
	"time"
)

// Limiter ограничивает неудачные попытки входа (docs/api.md):
// MaxFailures неудач за Window по ключу → блокировка на BaseLock, каждая следующая вдвое дольше, до MaxLock.
// Состояние в памяти: после рестарта счётчики обнуляются, для одного процесса этого достаточно.
type Limiter struct {
	MaxFailures int
	Window      time.Duration
	BaseLock    time.Duration
	MaxLock     time.Duration

	mu      sync.Mutex
	entries map[string]*limitEntry
	lastGC  time.Time
}

type limitEntry struct {
	failures    []time.Time
	locks       int // сколько раз уже блокировали подряд: даёт рост паузы
	lockedUntil time.Time
	lastSeen    time.Time
}

func NewLimiter(maxFailures int, window, baseLock, maxLock time.Duration) *Limiter {
	return &Limiter{MaxFailures: maxFailures, Window: window, BaseLock: baseLock, MaxLock: maxLock,
		entries: map[string]*limitEntry{}}
}

// Allowed говорит, можно ли сейчас пробовать. Если нельзя — сколько ждать.
func (l *Limiter) Allowed(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || !now.Before(e.lockedUntil) {
		return true, 0
	}
	return false, e.lockedUntil.Sub(now)
}

// Fail записывает неудачу. Возвращает длительность новой блокировки, если она началась.
func (l *Limiter) Fail(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(now)

	e := l.entries[key]
	if e == nil {
		e = &limitEntry{}
		l.entries[key] = e
	}
	e.lastSeen = now

	cutoff := now.Add(-l.Window)
	kept := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	// Давно не ошибался после последней блокировки — рост паузы начинается заново.
	if len(kept) == 0 && e.locks > 0 && now.Sub(e.lockedUntil) > l.Window {
		e.locks = 0
	}
	e.failures = append(kept, now)

	if len(e.failures) < l.MaxFailures {
		return 0
	}
	lock := l.BaseLock << e.locks
	if lock > l.MaxLock || lock <= 0 {
		lock = l.MaxLock
	}
	e.locks++
	e.failures = e.failures[:0]
	e.lockedUntil = now.Add(lock)
	return lock
}

// Reset сбрасывает ключ после успешного входа.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// gc раз в Window выкидывает ключи, по которым давно ничего не было.
func (l *Limiter) gc(now time.Time) {
	if now.Sub(l.lastGC) < l.Window {
		return
	}
	l.lastGC = now
	for k, e := range l.entries {
		if now.Sub(e.lastSeen) > l.Window+l.MaxLock && !now.Before(e.lockedUntil) {
			delete(l.entries, k)
		}
	}
}
