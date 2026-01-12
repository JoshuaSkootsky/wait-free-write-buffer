# Wait-Free Write Buffer

[![Go Reference](https://pkg.go.dev/badge/github.com/JoshuaSkootsky/wait-free-write-buffer.svg)](https://pkg.go.dev/github.com/JoshuaSkootsky/wait-free-write-buffer) [![Go Report Card](https://goreportcard.com/badge/github.com/JoshuaSkootsky/wait-free-write-buffer)](https://goreportcard.com/report/github.com/JoshuaSkootsky/wait-free-write-buffer)



A high-performance, lock-free ring buffer for single-writer, multiple-reader scenarios where write latency is critical and bounded memory is required.

## Features

- **Wait-Free Writes**: Writer never blocks or fails (O(1) guaranteed)
- **Multiple Readers**: Each reader tracks its own position with zero contention
- **Gap Detection**: Readers detect when they've fallen behind and data was overwritten
- **Cache-Line Aligned**: Memory layout optimized to prevent false sharing
- **Zero Allocations**: No heap allocations on hot path after initialization
- **Generic**: Works with any comparable type

## Installation

	go get github.com/JoshuaSkootsky/wait-free-write-buffer

## Quick Start

	package main
	
	import (
	    "fmt"
	    "time"
	    rb "github.com/JoshuaSkootsky/wait-free-write-buffer"
	)
	
	type MarketData struct {
	    Symbol string
	    Price  float64
	    Volume int
	}
	
	func main() {
	    buffer := rb.NewRingBuffer[MarketData](65536)
	    
	    go func() {
	        for i := 0; i < 1000; i++ {
	            buffer.Write(MarketData{
	                Symbol: "AAPL",
	                Price:  150.0 + float64(i),
	                Volume: 1000 + i,
	            })
	            time.Sleep(1 * time.Microsecond)
	        }
	    }()
	    
	    go func() {
	        var cursor uint64  // Each reader MUST have its own cursor variable
	        for {
	            if data, ok := buffer.Read(&cursor); ok {
	                fmt.Printf("Read: %+v\n", data)
	            } else {
	                time.Sleep(10 * time.Microsecond)
	            }
	        }
	    }()
	    
	    time.Sleep(2 * time.Second)
	}

## API Reference

### Creating a Buffer

	func NewRingBuffer[T any](size uint64) *RingBuffer[T]

Creates a new ring buffer with size slots. Size must be a power of 2. Panics otherwise.

```go
// NewRingBuffer validates size is power of 2
if size&(size-1) != 0 {
    panic("size must be power of 2")
}
```

### Writing Data

	func (rb *RingBuffer[T]) Write(data T)

Writes data to the buffer. Wait-free and always succeeds. If the buffer is full, overwrites the oldest data. Must be called from a single writer goroutine.

### Reading Data

	func (rb *RingBuffer[T]) Read(cursor *uint64) (T, bool)

Attempts to read the next item for this reader. Returns (data, true) on success, (zero, false) if no new data is available or a gap was detected. Each reader must maintain its own cursor variable.

	func (rb *RingBuffer[T]) ReadWithGap(cursor *uint64, gapStart *uint64, gapEnd *uint64) (T, bool)

Extended read with gap detection. If a gap is detected, gapStart and gapEnd are set to the lost sequence range.

### Utility Methods

	func (rb *RingBuffer[T]) Len() uint64
	func (rb *RingBuffer[T]) Capacity() uint64
	func (rb *RingBuffer[T]) WriterCursor() uint64
	func (rb *RingBuffer[T]) Mask() uint64

## Architecture

	┌─────────────────────────────────────────────────────────────┐
	│                        RingBuffer[T]                        │
	├─────────────────────────────────────────────────────────────┤
	│  ┌─────────────────────────────────────────────────────┐    │
	│  │                 writerCursor (64 bytes)             │    │
	│  │                 atomic.Uint64                       │    │
	│  └─────────────────────────────────────────────────────┘    │
	│  ┌─────────────────────────────────────────────────────┐    │
	│  │                     Slot[T] x N                     │    │
	│  │  ┌─────────┬───────────────────────────────┐        │    │
	│  │  │sequence │ data [padded to 64 bytes]     │        │    │
	│  │  │(8 bytes)│                               │        │    │
	│  │  └─────────┴───────────────────────────────┘        │    │
	│  │                                                     │    │
	│  │  Each slot is 64 bytes for cache-line alignment     │    │
	│  └─────────────────────────────────────────────────────┘    │
	└─────────────────────────────────────────────────────────────┘

### Sequence-Based Synchronization

**Write:**
1. Increment writerCursor to get sequence number
2. Calculate slot index: `seq & mask`
3. Write data to slot
4. Update slot sequence: `slot.sequence = seq`

**Read:**
1. Calculate expected sequence: `cursor + 1`
2. Check slot sequence matches expected
3. If match: read data, update cursor
4. If mismatch: no data or gap detected

## Performance

**Verified January 12, 2026** on Intel(R) Core(TM) i5-10600K CPU @ 4.10GHz, Go 1.21:

| Operation | Latency | Throughput |
|-----------|---------|------------|
| Write | 13-28 ns/op | ~36-77M ops/s |
| Read | 90-108 ns/op | ~9-11M ops/s |

> **Note**: Read benchmark uses 65536 iterations (buffer size) with cursor reset per iteration to avoid wrap-around gaps. Real-world concurrent reader performance may vary based on cache effects and CPU frequency scaling.

### Slow Consumer Test

Real-world scenarios often involve readers that cannot keep up with writers. This test validates behavior under producer/consumer mismatch with skip-ahead recovery:

```go
func TestRingBuffer_EndToEnd_SlowConsumer(t *testing.T) {
    // Writer with 100ns delay (~3M ops/s)
    // Reader with 100ns delay per item
    // Gap skip-ahead: cursor = gapEnd to recover
    // Max lag tracking: measures buffer depth utilization
}
```

**Verified January 12, 2026** on Intel(R) Core(TM) i5-10600K CPU @ 4.10GHz:

| Metric | Value | Interpretation |
|--------|-------|----------------|
| Writes | ~600K items | Writer at ~3M ops/s (with 100ns sleep) |
| Reads | ~500K items | 82% recovery rate with skip-ahead |
| Max lag | 65535 items | Full buffer depth utilized |
| Gaps | 1 | Single skip-ahead event |

With skip-ahead recovery (`cursor = gapEnd`), the reader maintains 80%+ throughput. The max lag equals the buffer size (65536), showing the buffer is fully utilized before wrap-around.

### Recovery Scenario Test

Tests reader recovery after falling behind:

```go
func TestRingBuffer_RecoveryScenario(t *testing.T) {
    // Simulates temporary slowdown, then speed-up
    // Measures recovery rate after skip-ahead
}
```

**Verified Results:**
- Recovery rate: 83.4%
- Gaps detected: 1 (single skip-ahead event)
- Reader recovers to near-writer throughput after gap

**Performance Factors:**
- Buffer size: Larger buffers reduce wrap-around frequency
- Data type size: Smaller types copy faster; consider pointers for large structs
- Cache locality: Sequential access pattern maximizes cache hit rate
- CPU isolation: Pin writer to dedicated core with `runtime.LockOSThread()`

## Gap Recovery

Since bounded buffers cannot prevent drops, implement recovery:

	type GapHandler interface {
	    OnGap(start, end uint64)  // Request snapshot for lost range
	}
	
	var cursor uint64
	var gapStart, gapEnd uint64
	
	for {
	    data, ok := buffer.ReadWithGap(&cursor, &gapStart, &gapEnd)
	    
	    switch {
	    case ok:
	        process(data)
	    case gapStart > 0:
	        handler.OnGap(gapStart, gapEnd)  // Expensive recovery
	        gapStart, gapEnd = 0, 0          // Reset gap markers
	    default:
	        runtime.Gosched()
	    }
	}

**Recovery Strategies:**
- Snapshot + Replay: Request snapshot from upstream, replay missed data
- Forward Error Correction: Request specific sequence ranges
- Accept Loss: For some use cases, log gap and continue

**Example: OnGap Implementation**

```go
type GapHandler struct {
    client *UpstreamClient
}

func (h *GapHandler) OnGap(start, end uint64) {
    // Request snapshot for lost range from upstream source
    h.client.RequestSnapshot(start, end)
}

// Usage with ring buffer
var cursor uint64
var gapStart, gapEnd uint64

for {
    data, ok := buffer.ReadWithGap(&cursor, &gapStart, &gapEnd)
    
    switch {
    case ok:
        process(data)
    case gapStart > 0:
        handler.OnGap(gapStart, gapEnd)  // Network call to upstream
        gapStart, gapEnd = 0, 0          // Reset gap markers
    default:
        runtime.Gosched()
    }
}
```

## When to Use This

**Use when:**
- Single writer, multiple readers
- Write latency is critical (cannot block)
- Bounded memory is required
- Occasional data loss is acceptable or recoverable

**Don't use when:**
- Multiple writers needed (use channels + mutex)
- Every item must be processed (use channels + backpressure)
- Readers need `select`/`timeout` semantics
- Data type is larger than a cache line (use pointers)

## Why Not Channels?

Go's channels add 50-200ns latency via mutexes, syscalls, and scheduler interactions:

| Operation     | Channel   | Ring Buffer (Verified) |
|---------------|-----------|------------------------|
| Write latency | 50-200ns  | ~13-28ns               |
| Read latency  | 50-200ns  | ~90-108ns              |
| Blocking      | Possible  | Never                  |
| Multiple readers | Fan-out overhead | Per-reader cursors |
| Bounded memory | No        | Yes                    |
| Gap detection | No        | Yes                    |

Rule of thumb: Use channels for coordination, this buffer for high-throughput data streaming.

## Thread Safety

- **Single writer**: Write method must be called from at most one goroutine
- **Multiple readers**: Each reader must use its own cursor variable (goroutine-local state)
- **No shared reader state**: Eliminates reader-reader contention
- **Lock-free**: No mutexes, condition variables, or channels on hot path

## Memory Ordering

Go's atomic package provides acquire/release semantics:
- Store in Write acts as a release barrier
- Load in Read acts as an acquire barrier

This ensures data written before `Store(sequence)` is visible after `Load(sequence)`.

## Use Cases

- Market data feeds (stock prices, order books)
- High-frequency trading systems
- Real-time audio/video streaming
- Telemetry and metrics collection
- Event sourcing with bounded memory
- Any system requiring single-producer, multi-consumer with minimal latency

### Example: Game Server Player Tracking

A game server tracking 1000+ players at 60+ updates/second exemplifies this pattern:

**Writer**: Physics engine computing positions (cannot stall at 60 FPS)

**Readers**:
- Network sender (latency-critical)
- AI bots (gap-tolerant)
- Replay recorder (gap-sensitive, needs recovery)

**Why not channels/mutexes?**
- 1000 channels × 50ns = 50μs overhead (unacceptable at 60 FPS)
- Mutex could block writer, causing frame drops
- No explicit gap detection

**Key insight**: Overwrite is a feature, not a bug. Rendering 2-frame-old positions causes "rubber-banding." Better to show latest state and detect gaps for readers that need completeness.

## Limitations

- Single writer only: Multiple writers require external synchronization
- Data dropped silently on wrap: Use ReadWithGap for detection
- Power-of-2 sizing: Required for fast modulo via bitmask
- No select/timeout support: Readers must spin or implement their own timing
- Generic type restrictions: Type must be assignable and not contain internal pointers that could race
