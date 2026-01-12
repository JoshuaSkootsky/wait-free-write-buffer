package ringbuffer

import (
	"sync"
	"sync/atomic"
	"testing"
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
	rb := New[int](65536)

	for i := 0; i < b.N; i++ {
		rb.Write(i)
	}

	var cursor uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for {
			if data, ok := rb.Read(&cursor); ok {
				_ = data
				break
			}
		}
	}
}
