# Wait-Free Write Buffer

[![Go Reference](https://pkg.go.dev/badge/github.com/JoshuaSkootsky/wait-free-write-buffer.svg)](https://pkg.go.dev/github.com/JoshuaSkootsky/wait-free-write-buffer) [![Go Report Card](https://goreportcard.com/badge/github.com/JoshuaSkootsky/wait-free-write-buffer)](https://goreportcard.com/report/github.com/JoshuaSkootsky/wait-free-write-buffer) [![License: MPL-2.0](https://img.shields.io/badge/License-MPL-2.0-blue.svg)](LICENSE)

## License: MPL-2.0 OR Commercial

This library is dual-licensed under:

| Use Case | License | Cost | Requirements |
|----------|---------|------|--------------|
| Open source projects | MPL-2.0 | Free | Must open-source your application |
| Commercial use | Commercial | $5,000/year | No open-source requirement |

**MPL-2.0**: Strong copyleft license. Free to use, but if you deploy this
software as part of a networked service, you must release your application's
source code under MPL-2.0 or compatible terms.

**Commercial License**: Private use without open-source obligations.
Includes priority support and custom integration assistance.

[Get commercial license](mailto:joshua.skootsky@gmail.com) | [MPL-2.0 License](LICENSE)

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

```go
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
	// Create buffer with power-of-2 size
	buffer := rb.New[MarketData](65536)

	// Writer goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			buffer.Write(MarketData{
				Symbol: "AAPL",
				Price:  150.0 + float64(i),
				Volume: 1000 + i,
			})
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Reader goroutine - each reader needs its own cursor
	go func() {
		var cursor uint64
		for {
			if data, ok := buffer.Read(&cursor); ok {
				fmt.Printf("Read: %+v\n", data)
			} else {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	time.Sleep(2 * time.Second)
}
```

## Demo

A complete working example demonstrating the wait-free write buffer:
[https://github.com/JoshuaSkootsky/demo-wait-free-write-buffer](https://github.com/JoshuaSkootsky/demo-wait-free-write-buffer)

## Core Concepts

### The Cursor

Each reader must maintain its own `cursor` variable. This is a uint64 that tracks the last sequence number read:

```go
var cursor uint64  // Each reader needs its own cursor!
```

The cursor is **goroutine-local state**. Multiple readers sharing a cursor will read duplicate data.

### Write Semantics

- Always succeeds, never blocks
- If buffer is full, oldest data is overwritten
- Must be called from a single goroutine only

### Read Semantics

- Returns `(data, true)` when new data is available
- Returns `(zero, false)` when no new data OR when a gap was detected
- Check `cursor` to distinguish between "no data yet" and "data was dropped"

## API Reference

### Creating a Buffer

```go
func New[T any](size uint64) *RingBuffer[T]
```

Creates a new ring buffer with `size` slots. Size must be a power of 2. Panics otherwise.

```go
buffer := rb.New[MyType](1024)  // 1024 slots
```

### Writing Data

```go
func (rb *RingBuffer[T]) Write(data T)
```

Writes data to the buffer. Wait-free and always succeeds. If the buffer is full, overwrites the oldest data. Must be called from a single writer goroutine.

### Reading Data

```go
func (rb *RingBuffer[T]) Read(cursor *uint64) (T, bool)
```

Attempts to read the next item for this reader.

- Returns `(data, true)` on success
- Returns `(zero, false)` if no new data is available OR if a gap was detected

Each reader must maintain its own cursor variable:

```go
var cursor uint64
for {
    if data, ok := buffer.Read(&cursor); ok {
        fmt.Println(data)
    }
    // cursor is updated inside Read() on success
}
```

### Reading with Gap Detection

```go
func (rb *RingBuffer[T]) ReadWithGap(cursor *uint64, gapStart, gapEnd *uint64) (T, bool)
```

Extended read that detects when data has been overwritten.

- Returns `(data, true)` on success
- Returns `(zero, false)` if no new data
- If a gap is detected, sets `gapStart` and `gapEnd` to the lost sequence range

```go
var cursor uint64
var gapStart, gapEnd uint64

for {
    data, ok := buffer.ReadWithGap(&cursor, &gapStart, &gapEnd)

    switch {
    case ok:
        process(data)
    case gapStart > 0:
        // Data was dropped! Recover from gapStart to gapEnd
        handleGap(gapStart, gapEnd)
        gapStart, gapEnd = 0, 0  // Reset gap markers
    default:
        // No data available yet
    }
}
```

### Utility

```go
func (rb *RingBuffer[T]) Capacity() uint64
```

Returns the buffer capacity (number of slots).

```go
func (rb *RingBuffer[T]) Mask() uint64
```

Returns the bitmask used for indexing (size - 1). Useful for calculating sequence numbers.

## Gap Recovery

Since the buffer has bounded size, data will be overwritten when the writer overtakes readers. Use `ReadWithGap` to detect and recover from gaps:

```go
var cursor uint64
var gapStart, gapEnd uint64

for {
    data, ok := buffer.ReadWithGap(&cursor, &gapStart, &gapEnd)

    if ok {
        process(data)
    } else if gapStart > 0 {
        // Lost data from gapStart to gapEnd
        recoverFromSnapshot(gapStart, gapEnd)
        cursor = gapEnd  // Skip to end of gap
        gapStart, gapEnd = 0, 0
    }
    // else: no data available yet
}
```

Recovery strategies:
- **Snapshot + Replay**: Request missed data from upstream
- **Forward Error Correction**: Request specific sequence ranges
- **Accept Loss**: Log and continue (acceptable for some metrics)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        RingBuffer[T]                        │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────────────────────────────────────────────┐    │
│  │              writerCursor (atomic.Uint64)           │    │
│  │         Incremented on each write (1, 2, 3...)     │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              Slot[T] array (size N)                 │    │
│  │  ┌─────────┬──────────────────────────────────┐    │    │
│  │  │sequence │ data (padded to 64 bytes)        │    │    │
│  │  │ (8 B)   │                                  │    │    │
│  │  └─────────┴──────────────────────────────────┘    │    │
│  │                                                     │    │
│  │  Each slot is 64 bytes for cache-line alignment    │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

### How It Works

**Write:**
1. Atomically increment `writerCursor` to get sequence number
2. Calculate slot index: `seq & mask`
3. Write data to slot
4. Store sequence number (acts as release barrier)

**Read:**
1. Calculate expected sequence: `cursor + 1`
2. Calculate slot index: `expected & mask`
3. Load slot sequence
4. If `slot.sequence == expected`: read data, update cursor
5. If `slot.sequence < expected`: no data yet (wrap-around)
6. If `slot.sequence > expected`: gap detected (data was dropped)

## Performance

**Verified January 12, 2026** on Intel(R) Core(TM) i5-10600K CPU @ 4.10GHz, Go 1.21:

| Operation | Latency | Throughput |
|-----------|---------|------------|
| Write | 13-28 ns/op | ~36-77M ops/s |
| Read | 90-108 ns/op | ~9-11M ops/s |

### Slow Consumer Behavior

When a reader cannot keep up with the writer:

- Writer continues without blocking (overwrites oldest data)
- Reader detects gap on next read
- After gap, reader recovers to near-writer throughput
- Max lag equals buffer size before complete wrap-around

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

## Common Mistakes

### 1. Sharing cursors between readers

```go
// WRONG: Shared cursor
var cursor uint64
go reader1(&cursor)
go reader2(&cursor)  // Both readers get same data!
```

```go
// CORRECT: Each reader has its own cursor
var cursor1 uint64
var cursor2 uint64
go reader1(&cursor1)
go reader2(&cursor2)
```

### 2. Modifying cursor outside Read

```go
// WRONG
var cursor uint64
buffer.Read(&cursor)
cursor = 100  // Don't do this!
```

The cursor is updated by `Read()` automatically. Only modify it when recovering from a gap.

### 3. Using non-power-of-2 size

```go
// WRONG: Panics
buffer := rb.New[MyType](1000)
```

```go
// CORRECT
buffer := rb.New[MyType](1024)  // or 2048, 4096, etc.
```

## Thread Safety

- **Single writer**: `Write()` must be called from at most one goroutine
- **Multiple readers**: Each reader uses its own cursor (no shared state)
- **Lock-free**: No mutexes or channels on hot path
- **Memory ordering**: Go's atomic package provides acquire/release semantics

## Use Cases

- Market data feeds (stock prices, order books)
- High-frequency trading systems
- Real-time audio/video streaming
- Telemetry and metrics collection
- Game server state updates
- Event sourcing with bounded memory
