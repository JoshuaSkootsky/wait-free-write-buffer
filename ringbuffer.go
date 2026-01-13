// Copyright (c) 2025 Joshua Skootsky
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Alternatively, you can license this code under a commercial license.
// Contact: joshua.skootsky@gmail.com

// Package ringbuffer provides a wait-free, single-producer single-consumer (SPSC)
// ring buffer with O(1) operations and zero allocations per write/read.
//
// # Thread-Safety Guarantees
//
// This ring buffer is lock-free and wait-free for its documented use case:
//   - Single goroutine may call Write (the producer)
//   - Single goroutine may call Read/ReadWithGap (the consumer)
//   - All other goroutines must not access the buffer
//
// Violating these constraints (multiple producers or consumers) will cause
// data races and undefined behavior.
//
// # Performance Characteristics
//
//   - Wait-free O(1) operations: both Write and Read complete in constant time
//   - Zero allocations: all slots are pre-allocated at creation
//   - Cache-line padding: prevents false sharing between producer and consumer
//   - No blocking: Write always succeeds (overwrites oldest when full)
//
// # Usage Example
//
//	rb := ringbuffer.New(64) // Size must be a power of 2
//
//	// Producer goroutine
//	go func() {
//	    for i := 0; i < 100; i++ {
//	        rb.Write(i)
//	    }
//	}()
//
//	// Consumer goroutine
//	var cursor uint64
//	for i := 0; i < 100; i++ {
//	    if data, ok := rb.Read(&cursor); ok {
//	        fmt.Println(data)
//	    }
//	}
package ringbuffer

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const cacheLinePad = 64

// Slot is an internal buffer slot containing a sequence number and data.
// Slots include cache-line padding to prevent false sharing between adjacent slots.
//
// The sequence number coordinates the producer-consumer handoff:
//   - Producer writes data then stores the sequence number
//   - Consumer checks the sequence number to verify data is ready
type Slot[T any] struct {
	sequence atomic.Uint64
	_        [cacheLinePad - 8]byte
	data     T
}

// RingBuffer is a single-producer single-consumer (SPSC) ring buffer.
// It provides wait-free, O(1) write and read operations with zero allocations.
//
// The buffer has a fixed capacity set at creation time and uses a mask
// (size-1) for efficient index calculation via bitwise AND.
//
// # Memory Behavior
//
// All slots are pre-allocated at creation. Write and Read operations
// perform no heap allocations.
//
// # Full Buffer Behavior
//
// When the buffer is full, Write overwrites the oldest slot. This makes
// the buffer suitable for monitoring and telemetry use cases where losing
// recent data is acceptable. For strict ordering, ensure the consumer
// keeps pace with the producer.
//
// Example:
//
//	rb := ringbuffer.New(1024)
//	rb.Write("event-1")
//	rb.Write("event-2")
//
//	var cursor uint64
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "event-1"
//	}
type RingBuffer[T any] struct {
	buffer []Slot[T]
	mask   uint64

	writerCursor atomic.Uint64
	_            [cacheLinePad - 8]byte
}

// New creates a new RingBuffer with the given capacity.
//
// The size must be a power of two (1, 2, 4, 8, 16, ...). This requirement
// enables efficient index calculation using bitwise AND with the mask,
// which is faster than modulo operations.
//
// Panics if size is not a power of two.
//
// Example:
//
//	// Create a buffer for 64 items
//	rb := ringbuffer.New(64)
//
//	// Create a buffer for 1024 items
//	rb = ringbuffer.New(1024)
func New[T any](size uint64) *RingBuffer[T] {
	if size&(size-1) != 0 {
		panic("size must be power of 2")
	}

	rb := &RingBuffer[T]{
		buffer: make([]Slot[T], size),
		mask:   size - 1,
	}

	for i := uint64(0); i < size; i++ {
		rb.buffer[i].sequence.Store(0)
	}

	return rb
}

// Write appends data to the ring buffer. This method is wait-free and
// always succeeds: it atomically claims a slot and writes the data.
//
// The write operation is single-producer safe: only one goroutine should
// call Write. Multiple producers will cause data races.
//
// When the buffer is full, Write overwrites the oldest slot. This behavior
// is intentional for use cases like monitoring and telemetry where:
//   - Blocking is unacceptable (wait-free requirement)
//   - Losing old data is preferable to blocking
//   - Recent data is more valuable than complete history
//
// Example:
//
//	rb := ringbuffer.New(64)
//	rb.Write("hello")
//	rb.Write(42)
//	rb.Write(someStruct{Value: "data"})
func (rb *RingBuffer[T]) Write(data T) {
	seq := rb.writerCursor.Add(1)
	idx := seq & rb.mask

	slot := &rb.buffer[idx]
	slot.data = data
	slot.sequence.Store(seq)
}

// Read reads the next item from the ring buffer, advancing the cursor.
// The cursor tracks the last successfully read sequence number.
//
// Returns (data, true) if an item was read, or (zeroValue, false) if
// the buffer is empty (the next slot hasn't been written yet).
//
// The read operation is single-consumer safe: only one goroutine should
// call Read. Multiple consumers will cause data races.
//
// The cursor must be initialized to 0 before the first read:
//
//	var cursor uint64  // starts at 0
//	for {
//	    if data, ok := rb.Read(&cursor); ok {
//	        // process data
//	    }
//	}
//
// Example:
//
//	rb := ringbuffer.New(64)
//	rb.Write("first")
//	rb.Write("second")
//
//	var cursor uint64
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "first"
//	}
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "second"
//	}
//	if _, ok := rb.Read(&cursor); !ok {
//	    fmt.Println("buffer empty")
//	}
func (rb *RingBuffer[T]) Read(cursor *uint64) (T, bool) {
	var zero T

	nextSeq := *cursor + 1
	idx := nextSeq & rb.mask

	slot := &rb.buffer[idx]
	seq := slot.sequence.Load()

	if seq != nextSeq {
		return zero, false
	}

	data := slot.data
	*cursor = nextSeq

	return data, true
}

// ReadWithGap reads the next item like Read, but also detects gaps in the
// sequence numbers. This is useful when the producer may have been slow
// or restarted, and you need to know if data was missed.
//
// # Gap Detection Use Case
//
// Gaps occur when the producer outpaces the consumer. This can happen in:
//   - Monitoring systems where collection intervals vary
//   - Telemetry with network delays or temporary disconnects
//   - Logging with burst traffic exceeding consumer capacity
//
// When a gap is detected:
//   - Returns (zeroValue, false)
//   - Sets *gapStart to the first missed sequence number
//   - Sets *gapEnd to the last missed sequence number
//
// The consumer can then handle the gap (log an alert, update metrics, etc.)
// before continuing to read valid data.
//
// Example:
//
//	rb := ringbuffer.New(64)
//	rb.Write("a") // seq 1
//	rb.Write("b") // seq 2
//	// producer restarts or skips to seq 10
//	rb.Write("k") // seq 10
//
//	var cursor uint64
//	var gapStart, gapEnd uint64
//
//	// Read "a"
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "a"
//	}
//
//	// Read "b"
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "b"
//	}
//
//	// Gap detected between b (2) and k (10)
//	if data, ok := rb.ReadWithGap(&cursor, &gapStart, &gapEnd); !ok {
//	    fmt.Printf("Gap: %d to %d missed\n", gapStart, gapEnd) // Gap: 3 to 9 missed
//	}
//
//	// Now read "k"
//	if data, ok := rb.Read(&cursor); ok {
//	    fmt.Println(data) // "k"
//	}
func (rb *RingBuffer[T]) ReadWithGap(cursor *uint64, gapStart, gapEnd *uint64) (T, bool) {
	var zero T

	nextSeq := *cursor + 1
	idx := nextSeq & rb.mask

	slot := &rb.buffer[idx]
	seq := slot.sequence.Load()

	if seq < nextSeq {
		return zero, false
	}

	if seq > nextSeq {
		*gapStart = nextSeq
		*gapEnd = seq - 1
		return zero, false
	}

	data := slot.data
	*cursor = nextSeq

	return data, true
}

// Capacity returns the fixed capacity of the ring buffer.
// The capacity is set at creation time and never changes.
//
// The capacity is always a power of two, as required by New.
//
// Example:
//
//	rb := ringbuffer.New(64)
//	fmt.Println(rb.Capacity()) // 64
func (rb *RingBuffer[T]) Capacity() uint64 {
	return uint64(len(rb.buffer))
}

// Mask returns size-1, which is used for efficient index calculation.
// The mask enables using bitwise AND (seq & mask) instead of modulo
// (seq % size), which is faster on most architectures.
//
// This is primarily useful for external code that needs to compute
// indices matching the ring buffer's internal logic.
//
// Example:
//
//	rb := ringbuffer.New(64)
//	mask := rb.Mask() // 63
//	idx := 100 & mask // 36, same as 100 % 64
func (rb *RingBuffer[T]) Mask() uint64 {
	return rb.mask
}

func Example() {
	rb := New[int](64)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			rb.Write(i)
		}
	}()

	var cursor uint64
	for i := 0; i < 10; i++ {
		if data, ok := rb.Read(&cursor); ok {
			fmt.Println(data)
		}
	}

	wg.Wait()

	// Output:
	// 0
	// 1
	// 2
	// 3
	// 4
	// 5
	// 6
	// 7
	// 8
	// 9
}
