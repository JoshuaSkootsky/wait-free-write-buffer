// Copyright (c) 2025 Joshua Skootsky
//
// Licensed under the Business Source License 1.1
// You may use this file only in compliance with one of:
// 1. BSL-1.1 (non-production use is free)
// 2. Commercial License (contact for pricing)
//
// After 4 years (2029-01-01), this becomes Apache-2.0

package ringbuffer

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRingBuffer_WriteRead(t *testing.T) {
	rb := New[int](1024)

	var cursor uint64

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			rb.Write(i)
		}
	}()
	wg.Wait()

	for i := 0; i < 100; i++ {
		data, ok := rb.Read(&cursor)
		if !ok {
			t.Fatalf("read failed at index %d", i)
		}
		if data != i {
			t.Fatalf("expected %d, got %d", i, data)
		}
	}
}

func TestRingBuffer_EmptyRead(t *testing.T) {
	rb := New[int](1024)
	var cursor uint64

	_, ok := rb.Read(&cursor)
	if ok {
		t.Fatal("expected false for empty buffer")
	}
}

func TestRingBuffer_Overwrite(t *testing.T) {
	size := uint64(16)
	rb := New[int](size)

	var cursor uint64

	for i := uint64(0); i < size; i++ {
		rb.Write(int(i))
	}

	cursor = size - 1

	for i := size; i < size*2; i++ {
		rb.Write(int(i))
	}

	var gapStart, gapEnd uint64
	_, ok := rb.ReadWithGap(&cursor, &gapStart, &gapEnd)

	if ok {
		t.Fatalf("expected gap detection")
	}

	if gapStart != 16 || gapEnd != 31 {
		t.Fatalf("unexpected gap: %d-%d", gapStart, gapEnd)
	}
}

func TestRingBuffer_MultipleReaders(t *testing.T) {
	rb := New[uint64](1024)
	totalWrites := uint64(1000)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(0); i < totalWrites; i++ {
			rb.Write(i)
		}
	}()
	wg.Wait()

	var cursor1, cursor2 uint64
	var read1, read2 uint64

	done := make(chan struct{})
	go func() {
		for {
			if _, ok := rb.Read(&cursor1); ok {
				atomic.AddUint64(&read1, 1)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	go func() {
		for {
			if _, ok := rb.Read(&cursor2); ok {
				atomic.AddUint64(&read2, 1)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	for {
		if atomic.LoadUint64(&read1)+atomic.LoadUint64(&read2) >= totalWrites {
			break
		}
	}
	close(done)
}

func BenchmarkRingBuffer_Write(b *testing.B) {
	rb := New[int](65536)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rb.Write(42)
		}
	})
}

func BenchmarkRingBuffer_Read(b *testing.B) {
	const size = 65536
	rb := New[int](size)

	for i := 0; i < size; i++ {
		rb.Write(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var cursor uint64 = 0

		for j := 0; j < size; j++ {
			if _, ok := rb.Read(&cursor); !ok {
				b.Fatalf("gap at read %d, cursor=%d", j, cursor)
			}
		}
	}
}

func TestRingBuffer_EndToEnd_SlowConsumer(t *testing.T) {
	const size = 65536
	rb := New[int](size)

	var written, read, retries, gaps uint64
	var maxLag uint64

	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		seq := uint64(0)
		for {
			select {
			case <-stopCh:
				return
			default:
				rb.Write(int(seq))
				atomic.AddUint64(&written, 1)
				seq++
				time.Sleep(100 * time.Nanosecond)
			}
		}
	}()

	time.Sleep(10 * time.Millisecond)

	var cursor uint64
	timeout := time.After(200 * time.Millisecond)

	for {
		select {
		case <-timeout:
			t.Logf("End-to-end results:")
			t.Logf("  Written: %d", atomic.LoadUint64(&written))
			t.Logf("  Read: %d", atomic.LoadUint64(&read))
			t.Logf("  Retries: %d", atomic.LoadUint64(&retries))
			t.Logf("  Gaps detected: %d", atomic.LoadUint64(&gaps))
			t.Logf("  Max lag: %d items", atomic.LoadUint64(&maxLag))
			t.Logf("  Recovery strategy: skip gaps (cursor = gapEnd)")

			if read == 0 {
				t.Error("No items were read")
			}
			return

		default:
			var gapStart, gapEnd uint64
			if _, ok := rb.ReadWithGap(&cursor, &gapStart, &gapEnd); ok {
				atomic.AddUint64(&read, 1)
				lag := atomic.LoadUint64(&written) - cursor
				for lag > atomic.LoadUint64(&maxLag) {
					atomic.CompareAndSwapUint64(&maxLag, atomic.LoadUint64(&maxLag), lag)
				}
				time.Sleep(100 * time.Nanosecond)
			} else {
				atomic.AddUint64(&retries, 1)

				if gapStart != gapEnd {
					atomic.AddUint64(&gaps, 1)
					if atomic.LoadUint64(&gaps) == 1 {
						t.Logf("Gap [%d, %d] at cursor %d", gapStart, gapEnd, cursor)
					}
					t.Logf("Skipping gap [%d, %d]", gapStart, gapEnd)
					cursor = gapEnd
				}

				runtime.Gosched()
			}
		}
	}
}

func TestRingBuffer_RecoveryScenario(t *testing.T) {
	const size = 65536
	rb := New[int](size)

	var written, read, gaps uint64

	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		seq := uint64(0)
		for {
			select {
			case <-stopCh:
				return
			default:
				rb.Write(int(seq))
				atomic.AddUint64(&written, 1)
				seq++
				time.Sleep(100 * time.Nanosecond)
			}
		}
	}()

	time.Sleep(10 * time.Millisecond)

	var cursor uint64

	go func() {
		time.Sleep(50 * time.Millisecond)
		t.Log("Reader speeding up: reducing processing delay")
	}()

	timeout := time.After(200 * time.Millisecond)

	for {
		select {
		case <-timeout:
			t.Logf("Recovery scenario results:")
			t.Logf("  Written: %d", atomic.LoadUint64(&written))
			t.Logf("  Read: %d", atomic.LoadUint64(&read))
			t.Logf("  Gaps: %d", atomic.LoadUint64(&gaps))
			t.Logf("  Recovery rate: %.1f%%", float64(atomic.LoadUint64(&read))/float64(atomic.LoadUint64(&written))*100)
			return

		default:
			var gapStart, gapEnd uint64
			if _, ok := rb.ReadWithGap(&cursor, &gapStart, &gapEnd); ok {
				atomic.AddUint64(&read, 1)
				time.Sleep(100 * time.Nanosecond)
			} else {
				if gapStart != gapEnd {
					atomic.AddUint64(&gaps, 1)
					if atomic.LoadUint64(&gaps) == 1 {
						t.Logf("Gap [%d, %d] - recovering with skip-ahead", gapStart, gapEnd)
					}
					cursor = gapEnd
				}
				runtime.Gosched()
			}
		}
	}
}
