package game

import "math/rand/v2"

// newTestRNG mirrors the generator New installs so hand-built test simulations
// behave identically to real ones.
func newTestRNG(seed int64) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), 0x9E3779B97F4A7C15))
}
