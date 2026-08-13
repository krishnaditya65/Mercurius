// A hand-rolled, genuinely lock-free single-producer/single-consumer (SPSC)
// bounded ring buffer — FEATURES.md §9 "Lock-free ring buffer
// ingress/egress".
//
// WHY HAND-ROLLED, NOT A DEPENDENCY: `Cargo.toml` only pulls in `serde`/
// `serde_json` — no `crossbeam` (or any other lock-free-queue crate)
// already sits in the dependency graph. Per the task brief, a heavy new
// dependency isn't worth adding just for one bounded SPSC queue, so this is
// a from-scratch implementation of the well-known bounded SPSC ring-buffer
// algorithm (the same core idea as Folly's `ProducerConsumerQueue`, JCTools'
// `SpscArrayQueue`, and the `rtrb`/`ringbuf` crates): a fixed-size array of
// slots, one atomic index owned (written) by the producer, one atomic index
// owned (written) by the consumer, and `Acquire`/`Release` pairing on those
// two indices standing in for a mutex. Neither `push` nor `pop` ever blocks
// on a lock — the worst case is a bounded spin (see `push`/`pop` below).
//
// USAGE SHAPE: this crate uses it in exactly one way — `main.rs` splits one
// ring buffer into a `RingBufferProducerHandle`/`RingBufferConsumerHandle`
// pair, hands the producer half to the network thread and the consumer half
// to the matching-core thread (ingress), and a second buffer the other way
// round for egress. `split` is the only way to obtain either handle, and
// neither handle implements `Clone`, so "single producer / single consumer"
// is enforced structurally, not just by convention.
//
// SAFETY MODEL (read this before touching any `unsafe` block below):
// Every slot in `slots` is, at any instant, in exactly one of two states:
// "owned by the producer" (uninitialized, or already drained by a prior
// pop, safe to overwrite) or "owned by the consumer" (initialized by a
// completed push, safe to read/move out of exactly once). Ownership
// transfers happen ONLY at the two `Ordering::Release` stores below
// (`tailIndex.store` in `tryPush`, `headIndex.store` in `tryPop`), each
// paired with the corresponding `Ordering::Acquire` load on the other side
// (`headIndex.load` in `tryPush`'s full-check refresh, `tailIndex.load` in
// `tryPop`'s empty-check refresh). That Acquire/Release pairing is what
// makes this sound without a mutex: the Release store publishes everything
// the writer did to the slot before it (the value write, or the read/drop),
// and the matching Acquire load on the other thread guarantees it will not
// touch that slot until it observes the publish. This is the standard
// "SPSC ring buffer" correctness argument and is why every individual
// `unsafe` block below is sound — each one's comment restates the specific
// half of this argument that applies to it.

use std::cell::UnsafeCell;
use std::mem::MaybeUninit;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

/// The shared backing store. Never constructed directly by callers — use
/// [`splitIntoSpscProducerConsumerHandles`], which hands out exactly one
/// [`RingBufferProducerHandle`] and one [`RingBufferConsumerHandle`] wired
/// to the same instance via `Arc`.
struct LockFreeSpscRingBufferShared<T> {
    /// Fixed-size backing array. `UnsafeCell` because both the producer
    /// (writing) and the consumer (reading) need to reach into this array
    /// through a shared `&LockFreeSpscRingBufferShared<T>` (they only ever
    /// hold it via `Arc`, never `&mut`) — ordinary Rust aliasing rules
    /// would forbid that, so interior mutability is unavoidable here.
    /// `MaybeUninit<T>` because most slots, most of the time, do not hold a
    /// live `T` (either never written yet, or already popped) and it would
    /// be unsound to pretend otherwise.
    slots: Box<[UnsafeCell<MaybeUninit<T>>]>,
    /// `slots.len() - 1`. `slots.len()` is enforced to be a power of two at
    /// construction time so `index & capacityMask` is a cheap, correct
    /// replacement for `index % slots.len()`.
    capacityMask: usize,
    /// Monotonically increasing count of items ever popped. ONLY the
    /// consumer ever writes this field; the producer only ever reads it
    /// (via `Acquire`, to find out how much room it has).
    headIndex: AtomicUsize,
    /// Monotonically increasing count of items ever pushed. ONLY the
    /// producer ever writes this field; the consumer only ever reads it
    /// (via `Acquire`, to find out how much data is available).
    tailIndex: AtomicUsize,
}

// SAFETY: `LockFreeSpscRingBufferShared<T>` is shared between exactly two
// threads (one producer, one consumer) via `Arc`, and every access to the
// `UnsafeCell` slots is guarded by the Acquire/Release protocol documented
// in the module header — a slot is never read and written concurrently,
// only ever handed off. That handoff discipline is exactly what `Sync`
// requires callers to be able to rely on, so it is sound to assert it here
// even though `UnsafeCell` itself never implements `Sync` automatically.
// Requires `T: Send` because a value written by the producer thread is
// eventually read/dropped by the consumer thread — i.e. `T` crosses a
// thread boundary, which is precisely what `Send` gates.
unsafe impl<T: Send> Sync for LockFreeSpscRingBufferShared<T> {}

impl<T> Drop for LockFreeSpscRingBufferShared<T> {
    fn drop(&mut self) {
        // Both handles are gone (this only runs once the `Arc` refcount
        // hits zero), so `&mut self` here is exclusive — no concurrent
        // producer/consumer access is possible any more. Everything in
        // `[head, tail)` was written by a completed `tryPush` and never
        // removed by a `tryPop` (that's the loop invariant both maintain),
        // so every one of those slots holds a genuinely initialized `T`
        // that must be dropped to avoid leaking it; everything outside that
        // range is uninitialized (or already moved-out-of) and must NOT be
        // touched.
        let headIndex = *self.headIndex.get_mut();
        let tailIndex = *self.tailIndex.get_mut();
        let mut cursor = headIndex;
        while cursor != tailIndex {
            let slotIndex = cursor & self.capacityMask;
            // SAFETY: `slotIndex` is within `[head, tail)`, which per the
            // invariant above holds a live, never-yet-dropped `T` written
            // by a completed push. We have exclusive (`&mut self`) access
            // to the whole buffer here, so nothing else can be reading or
            // writing this slot concurrently.
            unsafe {
                (*self.slots[slotIndex].get()).assume_init_drop();
            }
            cursor = cursor.wrapping_add(1);
        }
    }
}

/// The producer (write) half of one ring buffer. Created only by
/// [`splitIntoSpscProducerConsumerHandles`]; deliberately not `Clone` so
/// "single producer" is a structural guarantee, not just documentation.
pub struct RingBufferProducerHandle<T> {
    shared: Arc<LockFreeSpscRingBufferShared<T>>,
    /// The producer's own private, non-atomic copy of the true tail index.
    /// Only the producer thread ever reads or writes this field (it is not
    /// shared), so it needs no synchronization at all — it's kept in sync
    /// with `shared.tailIndex` by every `tryPush`, which is the only place
    /// `shared.tailIndex` is ever written.
    localTailIndex: usize,
    /// The producer's cached view of `shared.headIndex`, refreshed (via an
    /// `Acquire` load) only when the cache says the buffer looks full —
    /// avoids an atomic load on every single push in the common case where
    /// the buffer has plenty of room.
    cachedHeadIndex: usize,
}

/// The consumer (read) half of one ring buffer. Created only by
/// [`splitIntoSpscProducerConsumerHandles`]; deliberately not `Clone` so
/// "single consumer" is a structural guarantee, not just documentation.
pub struct RingBufferConsumerHandle<T> {
    shared: Arc<LockFreeSpscRingBufferShared<T>>,
    /// Mirrors `RingBufferProducerHandle::localTailIndex`, but for the
    /// consumer's own private copy of the head index.
    localHeadIndex: usize,
    /// Mirrors `RingBufferProducerHandle::cachedHeadIndex`, but the
    /// consumer's cached view of `shared.tailIndex`.
    cachedTailIndex: usize,
}

/// Builds one ring buffer of `capacity` slots and splits it into its
/// producer/consumer halves. `capacity` must be a nonzero power of two
/// (needed for the `index & capacityMask` trick); panics otherwise, same as
/// this crate's other constructors panic on caller-provided invariants
/// (e.g. `OrderBookCore` assertions) rather than silently coercing bad
/// input.
pub fn splitIntoSpscProducerConsumerHandles<T>(
    capacity: usize,
) -> (RingBufferProducerHandle<T>, RingBufferConsumerHandle<T>) {
    assert!(
        capacity != 0 && capacity.is_power_of_two(),
        "lockFreeSpscRingBuffer capacity must be a nonzero power of two, got {capacity}"
    );

    let slots: Box<[UnsafeCell<MaybeUninit<T>>]> = (0..capacity)
        .map(|_| UnsafeCell::new(MaybeUninit::uninit()))
        .collect();

    let shared = Arc::new(LockFreeSpscRingBufferShared {
        slots,
        capacityMask: capacity - 1,
        headIndex: AtomicUsize::new(0),
        tailIndex: AtomicUsize::new(0),
    });

    let producerHandle = RingBufferProducerHandle {
        shared: Arc::clone(&shared),
        localTailIndex: 0,
        cachedHeadIndex: 0,
    };
    let consumerHandle = RingBufferConsumerHandle {
        shared,
        localHeadIndex: 0,
        cachedTailIndex: 0,
    };
    (producerHandle, consumerHandle)
}

impl<T> RingBufferProducerHandle<T> {
    /// Non-blocking push. Returns `Err(value)` (handing the value straight
    /// back, nothing lost) if the buffer is currently full instead of
    /// blocking or spinning — callers that want blocking behavior should
    /// use [`RingBufferProducerHandle::push`] instead.
    pub fn tryPush(&mut self, value: T) -> Result<(), T> {
        let capacity = self.shared.capacityMask.wrapping_add(1);
        if self.localTailIndex.wrapping_sub(self.cachedHeadIndex) == capacity {
            // Our cached view says full — but the consumer may have popped
            // since we last checked, so refresh from the real counter
            // before giving up.
            //
            // SAFETY/ORDERING: this `Acquire` load synchronizes-with the
            // consumer's `Ordering::Release` store to `headIndex` in
            // `tryPop`, so if it observes an updated head, it also
            // observes (happens-after) every `assume_init_read` the
            // consumer performed up to that point — i.e. we can safely
            // conclude those slots are free to overwrite.
            self.cachedHeadIndex = self.shared.headIndex.load(Ordering::Acquire);
            if self.localTailIndex.wrapping_sub(self.cachedHeadIndex) == capacity {
                return Err(value);
            }
        }

        let slotIndex = self.localTailIndex & self.shared.capacityMask;
        // SAFETY: this slot is exclusively ours to write right now. Only
        // the producer ever advances `tailIndex` / writes into a slot at
        // `tailIndex`, so no other producer can race us (there is only
        // one, by construction — `RingBufferProducerHandle` isn't
        // `Clone`). The full-check above proved this slot is not one the
        // consumer still owns (either it was never written, or the
        // consumer's `tryPop` already moved its previous value out and
        // published that via the `Release` store this function's Acquire
        // load synchronizes with), so it is safe to write a fresh value
        // into it without racing a concurrent read.
        unsafe {
            (*self.shared.slots[slotIndex].get()).write(value);
        }

        self.localTailIndex = self.localTailIndex.wrapping_add(1);
        // Publish: makes both the new tail value AND the slot write above
        // visible to a consumer that does the corresponding `Acquire` load
        // in `tryPop`. Everything from this thread up to and including the
        // slot write happens-before any consumer that observes this store.
        self.shared
            .tailIndex
            .store(self.localTailIndex, Ordering::Release);
        Ok(())
    }

    /// Blocking push: spins until there is room. The matching-engine wire-
    /// up (`main.rs`) sizes both ring buffers generously relative to how
    /// many requests can genuinely be in flight at once (the network side
    /// is a strictly sequential accept loop, so ingress depth is bounded by
    /// 1 in practice), so in steady state this never actually spins more
    /// than an instant — it exists for correctness under a burst, not as
    /// the primary flow-control mechanism.
    pub fn push(&mut self, mut value: T) {
        loop {
            match self.tryPush(value) {
                Ok(()) => return,
                Err(returnedValue) => {
                    value = returnedValue;
                    std::hint::spin_loop();
                }
            }
        }
    }
}

impl<T> RingBufferConsumerHandle<T> {
    /// Non-blocking pop. Returns `None` if the buffer is currently empty
    /// instead of blocking or spinning — callers that want blocking
    /// behavior should use [`RingBufferConsumerHandle::pop`] instead.
    pub fn tryPop(&mut self) -> Option<T> {
        if self.localHeadIndex == self.cachedTailIndex {
            // Our cached view says empty — refresh from the real counter
            // before giving up, same reasoning as `tryPush`'s full-check.
            //
            // SAFETY/ORDERING: this `Acquire` load synchronizes-with the
            // producer's `Ordering::Release` store to `tailIndex` in
            // `tryPush`, so if it observes an updated tail, it also
            // observes (happens-after) the slot write the producer
            // performed just before that store — i.e. we can safely
            // conclude the slot now holds a fully-initialized `T`.
            self.cachedTailIndex = self.shared.tailIndex.load(Ordering::Acquire);
            if self.localHeadIndex == self.cachedTailIndex {
                return None;
            }
        }

        let slotIndex = self.localHeadIndex & self.shared.capacityMask;
        // SAFETY: this slot is exclusively ours to read right now. Only
        // the consumer ever advances `headIndex` / reads a slot at
        // `headIndex`, so no other consumer can race us (there is only
        // one, by construction — `RingBufferConsumerHandle` isn't
        // `Clone`). The empty-check above proved this slot holds a value a
        // completed `tryPush` wrote and whose publish this function's
        // Acquire load synchronized with, so `assume_init_read` reads a
        // genuinely initialized `T`. It logically moves the value out
        // (leaving the slot's old bit pattern behind but never read again
        // until the producer's own `write` re-initializes it), matching
        // `MaybeUninit`'s documented "read once, then treat as
        // uninitialized again" pattern.
        let value = unsafe { (*self.shared.slots[slotIndex].get()).assume_init_read() };

        self.localHeadIndex = self.localHeadIndex.wrapping_add(1);
        // Publish: tells the producer this slot is free to reuse. See
        // `tryPush`'s full-check comment for the paired Acquire.
        self.shared
            .headIndex
            .store(self.localHeadIndex, Ordering::Release);
        Some(value)
    }

    /// Blocking pop: spins until an item is available. This is what the
    /// matching-core thread uses to wait for the next ingress item, and
    /// what the network thread uses to wait for the matching core's
    /// egress response to the request it just enqueued — see `main.rs`.
    pub fn pop(&mut self) -> T {
        loop {
            if let Some(value) = self.tryPop() {
                return value;
            }
            std::hint::spin_loop();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicBool;
    use std::thread;

    #[test]
    fn pushThenPopOnAnEmptyBufferPreservesFifoOrder() {
        let (mut producer, mut consumer) = splitIntoSpscProducerConsumerHandles::<i32>(4);
        assert_eq!(consumer.tryPop(), None, "must start empty");

        producer.push(1);
        producer.push(2);
        producer.push(3);

        assert_eq!(consumer.pop(), 1);
        assert_eq!(consumer.pop(), 2);
        assert_eq!(consumer.pop(), 3);
        assert_eq!(
            consumer.tryPop(),
            None,
            "must be empty again after draining"
        );
    }

    #[test]
    fn tryPushReturnsTheValueBackWhenTheBufferIsFull() {
        let (mut producer, mut consumer) = splitIntoSpscProducerConsumerHandles::<i32>(2);
        assert_eq!(producer.tryPush(10), Ok(()));
        assert_eq!(producer.tryPush(20), Ok(()));
        // Capacity 2, both slots occupied — must reject, not overwrite.
        assert_eq!(producer.tryPush(30), Err(30));

        assert_eq!(consumer.pop(), 10);
        // Freed exactly one slot.
        assert_eq!(producer.tryPush(30), Ok(()));
        assert_eq!(producer.tryPush(40), Err(40));

        assert_eq!(consumer.pop(), 20);
        assert_eq!(consumer.pop(), 30);
        assert_eq!(consumer.tryPop(), None);
    }

    #[test]
    fn wrapsAroundTheBackingArrayRepeatedlyWithoutLosingOrder() {
        let (mut producer, mut consumer) = splitIntoSpscProducerConsumerHandles::<i32>(4);
        // Push/pop one at a time, many more times than capacity, so the
        // ring index wraps around the backing array repeatedly.
        for round in 0..1000 {
            producer.push(round);
            assert_eq!(consumer.pop(), round);
        }
    }

    #[test]
    fn droppingBothHandlesDropsEveryStillBufferedValueExactlyOnce() {
        use std::sync::Mutex;

        struct DropRecorder<'a> {
            id: i32,
            dropped: &'a Mutex<Vec<i32>>,
        }
        impl Drop for DropRecorder<'_> {
            fn drop(&mut self) {
                self.dropped.lock().unwrap().push(self.id);
            }
        }

        let droppedIds: Mutex<Vec<i32>> = Mutex::new(Vec::new());
        {
            let (mut producer, mut consumer) = splitIntoSpscProducerConsumerHandles(4);
            producer
                .tryPush(DropRecorder {
                    id: 1,
                    dropped: &droppedIds,
                })
                .map_err(|_| ())
                .unwrap();
            producer
                .tryPush(DropRecorder {
                    id: 2,
                    dropped: &droppedIds,
                })
                .map_err(|_| ())
                .unwrap();
            // Pop and immediately drop #1, leaving only #2 still buffered
            // when both handles go out of scope below.
            drop(consumer.tryPop());
            drop(producer);
            drop(consumer);
        }

        let mut recorded = droppedIds.into_inner().unwrap();
        recorded.sort_unstable();
        assert_eq!(
            recorded,
            vec![1, 2],
            "both values must be dropped exactly once"
        );
    }

    /// Real concurrent stress test: an actual producer thread and an actual
    /// consumer thread, pushing/popping a large number of items through
    /// the SAME ring buffer at the same time, no artificial synchronization
    /// beyond the ring buffer itself. Asserts every item is received
    /// exactly once, in exactly the order it was sent (SPSC ring buffers
    /// must preserve FIFO order) — this is the property that would break
    /// first if the Acquire/Release pairing documented in the module
    /// header were wrong.
    #[test]
    fn concurrentProducerAndConsumerThreadsPreserveFifoOrderWithNoLossOrDuplication() {
        const ITEM_COUNT: usize = 2_000_000;
        const RING_CAPACITY: usize = 256;

        let (mut producer, mut consumer) =
            splitIntoSpscProducerConsumerHandles::<usize>(RING_CAPACITY);

        let producerThread = thread::spawn(move || {
            for item in 0..ITEM_COUNT {
                producer.push(item);
            }
        });

        let consumerThread = thread::spawn(move || {
            let mut received = Vec::with_capacity(ITEM_COUNT);
            for _ in 0..ITEM_COUNT {
                received.push(consumer.pop());
            }
            received
        });

        producerThread.join().expect("producer thread panicked");
        let received = consumerThread.join().expect("consumer thread panicked");

        assert_eq!(received.len(), ITEM_COUNT, "no item lost or duplicated");
        for (expected, actual) in (0..ITEM_COUNT).zip(received.iter()) {
            assert_eq!(
                expected, *actual,
                "FIFO order violated: expected {expected} but got {actual}"
            );
        }
    }

    /// A second concurrent stress test shaped like the real ingress/egress
    /// usage in `main.rs`: many small bursts with the producer occasionally
    /// racing ahead of a slower consumer (forcing `tryPush` to actually
    /// observe a full buffer and spin), run under a small ring capacity to
    /// maximize how often the wraparound and full/empty edges are
    /// exercised concurrently, plus a heap-allocated payload type (`String`)
    /// instead of a `Copy` `usize` so this also exercises the "move a
    /// non-trivially-droppable value across threads through the buffer"
    /// path, not just a `Copy` fast path.
    #[test]
    fn concurrentStressWithHeapAllocatedPayloadsAndABusyProducerNeverLosesOrReordersItems() {
        const ITEM_COUNT: usize = 200_000;
        const RING_CAPACITY: usize = 8;

        let (mut producer, mut consumer) =
            splitIntoSpscProducerConsumerHandles::<String>(RING_CAPACITY);
        let producerDone = Arc::new(AtomicBool::new(false));
        let producerDoneForConsumer = Arc::clone(&producerDone);

        let producerThread = thread::spawn(move || {
            for item in 0..ITEM_COUNT {
                producer.push(format!("item-{item}"));
            }
            producerDone.store(true, Ordering::Release);
        });

        let consumerThread = thread::spawn(move || {
            let mut received = Vec::with_capacity(ITEM_COUNT);
            while received.len() < ITEM_COUNT {
                if let Some(value) = consumer.tryPop() {
                    received.push(value);
                } else {
                    // Busy-spin, mirroring `pop`'s strategy, but via
                    // `tryPop` so this loop can also observe
                    // `producerDoneForConsumer` (unused for correctness,
                    // just documents why this doesn't deadlock even if the
                    // producer somehow finished early).
                    let _ = producerDoneForConsumer.load(Ordering::Acquire);
                    std::hint::spin_loop();
                }
            }
            received
        });

        producerThread.join().expect("producer thread panicked");
        let received = consumerThread.join().expect("consumer thread panicked");

        assert_eq!(received.len(), ITEM_COUNT);
        for (index, value) in received.iter().enumerate() {
            assert_eq!(*value, format!("item-{index}"));
        }
    }
}
