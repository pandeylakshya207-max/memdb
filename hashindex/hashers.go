package hashindex

// StringHasher returns an FNV-1a hasher for string keys.
// FNV-1a is fast, well-distributed, and has no dependencies.
func StringHasher() Hasher[string] {
	return fnv1aString
}

func fnv1aString(s string) uint64 {
	const (
		offset64 uint64 = 14695981039346656037
		prime64  uint64 = 1099511628211
	)
	h := offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// IntHasher returns a hasher for int keys using a mixing function that
// distributes sequential integers well (avoids trivial clustering when
// keys are small sequential integers with a power-of-two table size).
func IntHasher() Hasher[int] {
	return hashInt
}

func hashInt(k int) uint64 {
	// Murmur3 finalizer mix — good avalanche for integer keys.
	x := uint64(k)
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// Uint64Hasher returns a hasher for uint64 keys.
func Uint64Hasher() Hasher[uint64] {
	return hashUint64
}

func hashUint64(k uint64) uint64 {
	k ^= k >> 33
	k *= 0xff51afd7ed558ccd
	k ^= k >> 33
	k *= 0xc4ceb9fe1a85ec53
	k ^= k >> 33
	return k
}
