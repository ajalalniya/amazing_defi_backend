package simulate

import "math"

// RNG is mulberry32, matching the prototype bit-for-bit so backtests and
// depth histograms reproduce.
type RNG struct {
	a uint32
}

func Mulberry32(seed uint32) *RNG {
	return &RNG{a: seed}
}

func (r *RNG) Next() float64 {
	r.a += 0x6D2B79F5
	t := (r.a ^ (r.a >> 15)) * (1 | r.a)
	t = (t + (t^(t>>7))*(61|t)) ^ t
	return float64(t^(t>>14)) / 4294967296.0
}

// Gauss is Box-Muller using this RNG. Consumes two non-zero uniforms.
func Gauss(r *RNG) float64 {
	u, v := 0.0, 0.0
	for u == 0 {
		u = r.Next()
	}
	for v == 0 {
		v = r.Next()
	}
	return math.Sqrt(-2*math.Log(u)) * math.Cos(2*math.Pi*v)
}

// HashStr is FNV-1a 32-bit, matching the prototype's hashStr.
func HashStr(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ToUint32 is JS ToUint32 (used when the prototype does `n >>> 0`).
func ToUint32(f float64) uint32 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	f = math.Trunc(f)
	mod := math.Mod(f, 4294967296)
	if mod < 0 {
		mod += 4294967296
	}
	return uint32(mod)
}
