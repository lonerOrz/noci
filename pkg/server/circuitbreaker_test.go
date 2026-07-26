package server

import (
	"testing"
	"time"
)

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Hour)
	if cb.State() != CircuitClosed {
		t.Fatalf("initial state = %v, want CircuitClosed", cb.State())
	}

	// 2 failures: still closed
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitClosed {
		t.Fatalf("after 2 failures: state = %v, want CircuitClosed", cb.State())
	}

	// 3rd failure: opens
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatalf("after 3 failures: state = %v, want CircuitOpen", cb.State())
	}
}

func TestCircuitBreaker_OpenRejects(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Hour)
	cb.RecordFailure()
	cb.RecordFailure() // opens

	if cb.Allow() {
		t.Fatal("Allow() should return false when Open")
	}
}

func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Millisecond)
	cb.RecordFailure()
	cb.RecordFailure() // opens

	time.Sleep(15 * time.Millisecond)

	if !cb.Allow() {
		t.Fatal("Allow() should return true after timeout (transition to HalfOpen)")
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("state = %v, want CircuitHalfOpen", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenToClosed(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Millisecond)
	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // transitions to HalfOpen

	// 2 successes: closes (halfOpenMax=2)
	cb.RecordSuccess()
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("after 1 success: state = %v, want CircuitHalfOpen", cb.State())
	}
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Fatalf("after 2 successes: state = %v, want CircuitClosed", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Millisecond)
	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // transitions to HalfOpen

	cb.RecordFailure() // should reopen
	if cb.State() != CircuitOpen {
		t.Fatalf("after failure in HalfOpen: state = %v, want CircuitOpen", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Hour)
	cb.RecordFailure()
	cb.RecordFailure()

	cb.RecordSuccess() // resets failure count
	if cb.failures != 0 {
		t.Fatalf("failures = %d, want 0 after success", cb.failures)
	}

	// Need 3 more failures to open
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitClosed {
		t.Fatalf("state = %v, want CircuitClosed (only 2 failures since reset)", cb.State())
	}
}

func TestCircuitBreaker_AllowResetsSuccesses(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Millisecond)
	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // HalfOpen, successes reset to 0

	cb.RecordSuccess() // 1 success
	cb.Allow()         // should NOT reopen halfOpen counter; Allow in HalfOpen is a no-op for successes
	cb.RecordSuccess() // 2 successes → closes
	if cb.State() != CircuitClosed {
		t.Fatalf("state = %v, want CircuitClosed", cb.State())
	}
}

func TestCircuitBreaker_ZeroThreshold(t *testing.T) {
	cb := NewCircuitBreaker(0, time.Hour)
	// threshold=0: any failure opens immediately
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatalf("state = %v, want CircuitOpen with threshold=0", cb.State())
	}
}
