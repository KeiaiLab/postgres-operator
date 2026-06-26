/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// dialFunc abstracts net.DialTimeout for testing.
type dialFunc func(network, addr string, timeout time.Duration) (net.Conn, error)

// errCircuitOpen indicates a backend is temporarily fast-failed by the breaker.
var errCircuitOpen = fmt.Errorf("backend circuit open (recent repeated failures)")

// backendDialer dials shard backends with bounded retry/backoff and a per-backend
// circuit breaker. Why: when a shard is down, dialing it blocks up to the timeout
// (× retries) for *every* new client. After failThreshold consecutive failures the
// breaker "opens" the backend for a cooldown, fast-failing new attempts so the
// router degrades quickly (a graceful PG error) instead of stalling connections.
type backendDialer struct {
	timeout       time.Duration
	retries       int // additional attempts after the first
	backoff       time.Duration
	failThreshold int
	cooldown      time.Duration

	dial  dialFunc
	now   func() time.Time
	sleep func(time.Duration)

	mu       sync.Mutex
	breakers map[string]*breaker
}

type breaker struct {
	failures  int
	openUntil time.Time
}

// newBackendDialer builds a dialer with real net/clock. failThreshold<=0 disables
// the breaker; retries<0 is treated as 0.
func newBackendDialer(timeout, backoff, cooldown time.Duration, retries, failThreshold int) *backendDialer {
	if retries < 0 {
		retries = 0
	}
	return &backendDialer{
		timeout:       timeout,
		retries:       retries,
		backoff:       backoff,
		failThreshold: failThreshold,
		cooldown:      cooldown,
		dial:          net.DialTimeout,
		now:           time.Now,
		sleep:         time.Sleep,
		breakers:      map[string]*breaker{},
	}
}

// Dial connects to addr, applying the circuit breaker then bounded retry/backoff.
func (d *backendDialer) Dial(addr string) (net.Conn, error) {
	if d.isOpen(addr) {
		return nil, fmt.Errorf("%s: %w", addr, errCircuitOpen)
	}
	var lastErr error
	for attempt := 0; attempt <= d.retries; attempt++ {
		if attempt > 0 && d.backoff > 0 {
			d.sleep(d.backoff)
		}
		conn, err := d.dial("tcp", addr, d.timeout)
		if err == nil {
			d.onSuccess(addr)
			return conn, nil
		}
		lastErr = err
	}
	d.onFailure(addr)
	return nil, lastErr
}

func (d *backendDialer) isOpen(addr string) bool {
	if d.failThreshold <= 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.breakers[addr]
	return b != nil && d.now().Before(b.openUntil)
}

func (d *backendDialer) onSuccess(addr string) {
	d.mu.Lock()
	delete(d.breakers, addr)
	d.mu.Unlock()
}

func (d *backendDialer) onFailure(addr string) {
	if d.failThreshold <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.breakers[addr]
	if b == nil {
		b = &breaker{}
		d.breakers[addr] = b
	}
	b.failures++
	if b.failures >= d.failThreshold {
		b.openUntil = d.now().Add(d.cooldown)
	}
}
