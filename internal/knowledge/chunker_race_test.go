package knowledge

import (
	"sync"
	"testing"
)

func TestChunkParamsConcurrentReadWrite(t *testing.T) {
	// Ensure concurrent SetChunkParams + loadChunkParams does not race.
	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 100

	// Writers: change params concurrently.
	for g := 0; g < goroutines/2; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				SetChunkParams(200+seed%100, 1500+seed%1000, 100+seed%200, 0.5+float64(seed%50)/100)
			}
		}(g)
	}

	// Readers: load params concurrently.
	for g := 0; g < goroutines/2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cp := loadChunkParams()
				// Just verify the snapshot is internally consistent.
				if cp.shortChunk < 0 || cp.longChunk < 0 || cp.overlapChars < 0 {
					t.Errorf("negative param in snapshot: %+v", cp)
				}
				if cp.semanticThreshold < 0 || cp.semanticThreshold > 1 {
					t.Errorf("semanticThreshold out of range: %f", cp.semanticThreshold)
				}
			}
		}()
	}

	wg.Wait()

	// Restore defaults for other tests.
	SetChunkParams(200, 2000, 200, 0.75)
}

func TestSetChunkParamsBounds(t *testing.T) {
	// Set invalid values — should be ignored.
	origMin, origMax, origOverlap, origSem := GetChunkParams()

	SetChunkParams(30, 400, -1, 2.0) // all below minimums or out of range
	min, max, overlap, sem := GetChunkParams()
	if min != origMin {
		t.Errorf("min = %d, want %d (below 50 should be ignored)", min, origMin)
	}
	if max != origMax {
		t.Errorf("max = %d, want %d (below 500 should be ignored)", max, origMax)
	}
	if overlap != origOverlap {
		t.Errorf("overlap = %d, want %d (negative should be ignored)", overlap, origOverlap)
	}
	if sem != origSem {
		t.Errorf("sem = %f, want %f (>1 should be ignored)", sem, origSem)
	}

	// Valid values should be accepted.
	SetChunkParams(300, 3000, 400, 0.85)
	min2, max2, overlap2, sem2 := GetChunkParams()
	if min2 != 300 || max2 != 3000 || overlap2 != 400 || sem2 != 0.85 {
		t.Errorf("valid params not applied: (%d,%d,%d,%f)", min2, max2, overlap2, sem2)
	}

	// Restore defaults.
	SetChunkParams(origMin, origMax, origOverlap, origSem)
}
