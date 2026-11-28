// Serializes verification attempts, including shutdown from the service lifecycle
// (VerificationState.kt).
package main

import (
	"sync"
	"time"
)

type verificationResult int

const (
	verificationIgnored verificationResult = iota
	verificationConnected
	verificationRetry
)

type verificationState struct {
	mu         sync.Mutex
	enabled    bool
	generation uint64
	inFlight   bool
	retries    int
	lastDelay  time.Duration
}

func (v *verificationState) start() {
	v.mu.Lock()
	v.enabled = true
	v.mu.Unlock()
}

func (v *verificationState) request() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.generation++
	v.retries = 0
	return v.generation
}

func (v *verificationState) begin(ticket uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.enabled || v.inFlight || ticket != v.generation {
		return false
	}
	v.inFlight = true
	return true
}

func (v *verificationState) finish(ticket uint64, success bool) verificationResult {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.inFlight = false
	if !v.enabled {
		return verificationIgnored
	}
	if ticket != v.generation {
		return verificationRetry
	}
	if success {
		return verificationConnected
	}
	seconds := min64(2<<minInt(v.retries, 4), 30)
	v.retries++
	v.lastDelay = time.Duration(seconds) * time.Second
	return verificationRetry
}

func (v *verificationState) stop() {
	v.mu.Lock()
	v.enabled = false
	v.generation++
	v.retries = 0
	v.mu.Unlock()
}

func (v *verificationState) lastDelayValue() time.Duration {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lastDelay
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
