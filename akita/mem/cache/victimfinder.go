// package cache

// // A VictimFinder decides with block should be evicted
// type VictimFinder interface {
// 	FindVictim(set *Set) *Block
// }

// // LRUVictimFinder evicts the least recently used block to evict
// type LRUVictimFinder struct {
// }

// // NewLRUVictimFinder returns a newly constructed lru evictor
// func NewLRUVictimFinder() *LRUVictimFinder {
// 	e := new(LRUVictimFinder)
// 	return e
// }

// // FindVictim returns the least recently used block in a set
// func (e *LRUVictimFinder) FindVictim(set *Set) *Block {
// 	// First try evicting an empty block
// 	for _, block := range set.LRUQueue {
// 		if !block.IsValid && !block.IsLocked {
// 			return block
// 		}
// 	}

// 	for _, block := range set.LRUQueue {
// 		if !block.IsLocked {
// 			return block
// 		}
// 	}

// 	return set.LRUQueue[0]
// }

package cache

type VictimFinder interface {
	FindVictim(set *Set) *Block
}

// SRRIP (Static Re-Reference Interval Prediction) victim finder.
// It predicts how far in the future a block will be referenced again using a tiny counter (RRPV).
// Eviction picks any block whose RRPV == rrpvMax; if none, it "ages" all blocks (RRPV++) and retries.
//
// Notes:
//   - This implementation keeps per-block RRPV in a local map so you can drop it in
//     without first changing Block. If you later add `RRPV` to Block, swap the map
//     reads/writes with direct field access.
//   - On fills you should call OnFill(b) (sets RRPV=2 for SRRIP). On hits call OnHit(b) (sets RRPV=0).
//     If you don’t wire those yet, eviction still works because unknown blocks default to insertRRPV.
type SRRIPVictimFinder struct {
	rrpv map[*Block]uint8
}

const (
	rrpvMax    = uint8(3) // 2-bit RRPV: 0..3; 3 == "evict me"
	insertRRPV = uint8(2) // SRRIP inserts at RRPV=2 (conservative)
	hitRRPV    = uint8(0) // On hit, protect the block
)

// NewSRRIPVictimFinder returns a newly constructed SRRIP evictor.
func NewSRRIPVictimFinder() *SRRIPVictimFinder {
	return &SRRIPVictimFinder{rrpv: make(map[*Block]uint8)}
}

// OnHit should be called by the cache when a block is hit.
func (e *SRRIPVictimFinder) OnHit(b *Block) {
	e.rrpv[b] = hitRRPV
}

// OnFill should be called by the cache when a block is filled/inserted.
func (e *SRRIPVictimFinder) OnFill(b *Block) {
	e.rrpv[b] = insertRRPV
}

// FindVictim returns the SRRIP-selected victim in the set.
// Priority:
//  1. An invalid & unlocked block (free frame) – immediate return (like the LRU code).
//  2. Any block with RRPV==3 and not locked.
//  3. Otherwise, age all candidates (RRPV++) and retry until (2) succeeds.
//
// If everything is locked, fall back to the first entry to match the reference behavior.
func (e *SRRIPVictimFinder) FindVictim(set *Set) *Block {
	// 1) First try to find a free (invalid) and unlocked block.
	for _, b := range set.LRUQueue {
		if !b.IsValid && !b.IsLocked {
			// Initialize bookkeeping for previously unseen blocks.
			if _, ok := e.rrpv[b]; !ok {
				e.rrpv[b] = insertRRPV
			}
			return b
		}
	}

	// Helper to try finding an RRPV-max (3) victim.
	findRRPVMax := func() *Block {
		for _, b := range set.LRUQueue {
			if b.IsLocked {
				continue
			}
			if e.getRRPV(b) == rrpvMax {
				return b
			}
		}
		return nil
	}

	// 2) Try immediate victim with RRPV==3.
	if v := findRRPVMax(); v != nil {
		return v
	}

	// 3) Age until someone reaches RRPV==3.
	//    This will terminate in at most (rrpvMax - minRRPV) steps.
	for {
		e.ageAll(set)
		if v := findRRPVMax(); v != nil {
			return v
		}
		// In pathological cases where all blocks are locked, break like the LRU reference code.
		allLocked := true
		for _, b := range set.LRUQueue {
			if !b.IsLocked {
				allLocked = false
				break
			}
		}
		if allLocked {
			break
		}
	}

	// Match the LRU fallback behavior if everything ends up locked.
	// (Caller may still reject a locked block; this mirrors the given reference.)
	if len(set.LRUQueue) > 0 {
		return set.LRUQueue[0]
	}
	return nil
}

func (e *SRRIPVictimFinder) getRRPV(b *Block) uint8 {
	if v, ok := e.rrpv[b]; ok {
		return v
	}
	// Default unseen blocks to SRRIP's insert value.
	e.rrpv[b] = insertRRPV
	return insertRRPV
}

func (e *SRRIPVictimFinder) ageAll(set *Set) {
	for _, b := range set.LRUQueue {
		if b.IsLocked || !b.IsValid {
			continue
		}
		v := e.getRRPV(b)
		if v < rrpvMax {
			e.rrpv[b] = v + 1
		}
	}
}

// Optional but recommended:
func (e *SRRIPVictimFinder) Reset() { e.rrpv = make(map[*Block]uint8) }
