// pkg/vsa/vsa_test.go
package vsa

import (
	"sync"
	"testing"
)

func TestVSA_Basics(t *testing.T) {
	t.Run("New", func(t *testing.T) {
		v := New(100)
		if v.scalar != 100 || v.vector != 0 {
			t.Errorf("New(100) = %+v, want scalar=100, vector=0", v)
		}
	})

	t.Run("UpdateAndState", func(t *testing.T) {
		v := New(100)
		v.Update(10)
		v.Update(-5)
		v.Update(2)

		scalar, vector := v.State()
		if scalar != 100 || vector != 7 {
			t.Errorf("State() = (%d, %d), want (100, 7)", scalar, vector)
		}
	})

	t.Run("Available", func(t *testing.T) {
		testCases := []struct {
			name              string
			initialScalar     int64
			updates           []int64
			expectedVector    int64
			expectedAvailable int64
		}{
			{"Positive Vector", 1000, []int64{100, 50}, 150, 850},
			{"Negative Vector", 1000, []int64{-100, -50}, -150, 850},
			{"Zero Vector", 1000, []int64{100, -100}, 0, 1000},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				v := New(tc.initialScalar)
				for _, update := range tc.updates {
					v.Update(update)
				}
				if _, vector := v.State(); vector != tc.expectedVector {
					t.Errorf("Expected vector %d, got %d", tc.expectedVector, vector)
				}
				if available := v.Available(); available != tc.expectedAvailable {
					t.Errorf("Expected available %d, got %d", tc.expectedAvailable, available)
				}
			})
		}
	})
}

func TestVSA_CommitWorkflow(t *testing.T) {
	v := New(1000)
	threshold := int64(50)

	// 1. Update until just under the threshold
	v.Update(30)
	v.Update(19)

	shouldCommit, vectorToCommit := v.CheckCommit(threshold)
	if shouldCommit {
		t.Errorf("CheckCommit() returned true prematurely, vector: %d", vectorToCommit)
	}

	// 2. Update to meet and exceed the threshold
	v.Update(1) // vector is now 50
	shouldCommit, vectorToCommit = v.CheckCommit(threshold)
	if !shouldCommit {
		t.Error("CheckCommit() returned false when threshold was met")
	}
	if vectorToCommit != 50 {
		t.Errorf("CheckCommit() returned vector %d, want 50", vectorToCommit)
	}

	// 3. Simulate a successful commit
	v.Commit(vectorToCommit)

	// 4. Verify the state is correct after commit
	scalar, vector := v.State()
	if scalar != 1050 {
		t.Errorf("After commit, scalar is %d, want 1050", scalar)
	}
	if vector != 0 {
		t.Errorf("After commit, vector is %d, want 0", vector)
	}

	// 5. Verify available resources is correct
	available := v.Available()
	if available != 1050 {
		t.Errorf("After commit, available is %d, want 1050", available)
	}
}

// TestVSA_Concurrent tests that the VSA can be safely updated by multiple goroutines.
func TestVSA_Concurrent(t *testing.T) {
	// If this test fails, it will likely be caught by the Go race detector.
	// Run with `go test -race ./...`
	t.Parallel()

	v := New(0)
	numGoroutines := 100
	updatesPerGoroutine := 1000
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				v.Update(1)
			}
		}()
	}

	wg.Wait()

	expectedVector := int64(numGoroutines * updatesPerGoroutine)
	_, vector := v.State()

	if vector != expectedVector {
		t.Errorf("Concurrent updates resulted in vector %d, want %d", vector, expectedVector)
	}
}
