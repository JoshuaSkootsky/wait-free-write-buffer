// Copyright (c) 2025 Joshua Skootsky
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Alternatively, you can license this code under a commercial license.
// Contact: joshua.skootsky@gmail.com

package ringbuffer

import (
	"sync/atomic"
)

const cacheLinePad = 64

type Slot[T any] struct {
	sequence atomic.Uint64
	_        [cacheLinePad - 8]byte
	data     T
}

type RingBuffer[T any] struct {
	buffer []Slot[T]
	mask   uint64

	writerCursor atomic.Uint64
	_            [cacheLinePad - 8]byte
}

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

func (rb *RingBuffer[T]) Write(data T) {
	seq := rb.writerCursor.Add(1)
	idx := seq & rb.mask

	slot := &rb.buffer[idx]
	slot.data = data
	slot.sequence.Store(seq)
}

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

func (rb *RingBuffer[T]) Capacity() uint64 {
	return uint64(len(rb.buffer))
}

func (rb *RingBuffer[T]) Mask() uint64 {
	return rb.mask
}
