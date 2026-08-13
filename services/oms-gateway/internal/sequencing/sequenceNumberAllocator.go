// Package sequencing assigns the single global (or per-shard, once sharded
// per instrument per ARCHITECTURE.md §3.2) order sequence number, once,
// before an order is routed to a matching shard. This is what makes the
// matching engine's event log strictly ordered and replayable — see
// ARCHITECTURE.md §3.4.
package sequencing

import "sync/atomic"

// GlobalSequenceNumberAllocator hands out strictly increasing sequence
// numbers. Implemented with an atomic counter rather than a mutex since
// this is called on every single order submission and a lock here would
// become a contention point under load.
//
// TODO(real build): once matching shards by instrument (§3.2), this needs
// to become per-shard, allocated by a router that knows the instrument ->
// shard mapping, not one global counter for every instrument.
type GlobalSequenceNumberAllocator struct {
	nextSequenceNumberToAssign uint64
}

func NewGlobalSequenceNumberAllocatorStartingAtOne() *GlobalSequenceNumberAllocator {
	return &GlobalSequenceNumberAllocator{nextSequenceNumberToAssign: 0}
}

func (allocator *GlobalSequenceNumberAllocator) AllocateNextSequenceNumber() uint64 {
	return atomic.AddUint64(&allocator.nextSequenceNumberToAssign, 1)
}
