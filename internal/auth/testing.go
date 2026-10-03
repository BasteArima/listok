package auth

// UseFastHashingForTests делает argon2id дешёвым. Только для тестов:
// с боевыми параметрами (64 МиБ, t=3) каждый хеш занимает ~100 мс.
func UseFastHashingForTests() {
	defaultArgon = argonParams{memory: 64, time: 1, threads: 1, keyLen: 32, saltLen: 16}
}
