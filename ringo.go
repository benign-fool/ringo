package ringo

import (
	"errors"
	"sync"

	"github.com/glycerine/rbuf"
)

type RingBuffer struct {
	Buffer             *rbuf.FixedSizeRingBuf
	readLock           *sync.Cond
	writeLock          *sync.Cond
	bufLock            *sync.Mutex
	pendingWriteLength int
	pendingDiffLock    *sync.RWMutex
	pendingWriteLock   *sync.Mutex
	doneLock           *sync.RWMutex
	diff               int
	maxSize            int
	done               bool
}

//NewBuffer creates a new RingBuffer of size `size`. It returns a *RingBuffer
func NewBuffer(size int) *RingBuffer {
	return &RingBuffer{
		Buffer:             rbuf.NewFixedSizeRingBuf(size),
		readLock:           &sync.Cond{L: &sync.Mutex{}},
		writeLock:          &sync.Cond{L: &sync.Mutex{}},
		bufLock:            &sync.Mutex{},
		pendingWriteLength: 0,
		pendingDiffLock:    &sync.RWMutex{},
		pendingWriteLock:   &sync.Mutex{},
		doneLock:           &sync.RWMutex{},
		diff:               0,
		maxSize:            size,
		done:               false,
	}
}

//Write writes the given slice `p` into the calling *RingBuffer and returns the number of bytes written, and an error if any.  If the length of `p` is greater than the size of *RingBuffer, it returns an error.
func (this *RingBuffer) Write(p []byte) (int, error) {
	this.doneLock.RLock()
	if this.done {
		this.doneLock.RUnlock()
		return 0, errors.New("This buffer has been marked as done.  Use Reset() to reopen the buffer for writing")
	}
	this.doneLock.RUnlock()

	if len(p) > this.maxSize {
		return -1, errors.New("Size to write exceeds the buffer size")
	}

	this.writeLock.L.Lock()
	this.pendingDiffLock.Lock()
	this.pendingWriteLength = len(p)
	this.pendingDiffLock.Unlock()

	this.pendingWriteLock.Lock()
	for !this.isSafeToWrite(len(p)) {
		this.writeLock.Wait()
	}

	this.bufLock.Lock()
	n, err := this.Buffer.Write(p)
	if err != nil {
		this.writeLock.L.Unlock()
		this.pendingWriteLock.Unlock()
		return n, err
	}
	this.diff = this.diff + n
	this.pendingWriteLock.Unlock()
	this.writeLock.L.Unlock()

	this.bufLock.Unlock()

	this.pendingDiffLock.Lock()
	this.pendingWriteLength = 0
	this.pendingDiffLock.Unlock()
	this.readLock.Signal()

	return n, err
}

//Read reads up to `len(p)` bytes from the calling *RingBuffer into `p` and returns the number of bytes read, and an error, if any.
//It also frees up the same number of bytes to now be written into the RingBuffer
func (this *RingBuffer) Read(p []byte) (int, error) {
	this.readLock.L.Lock()
	for !this.isSafeToRead(len(p)) {
		this.readLock.Wait()
	}

	this.bufLock.Lock()
	n, err := this.Buffer.Read(p)
	if err != nil {
		return n, err
	}
	this.diff = this.diff - n
	this.readLock.L.Unlock()

	this.pendingDiffLock.RLock()
	if this.maxSize-this.diff >= this.pendingWriteLength {
		this.writeLock.Signal()
	}
	this.pendingDiffLock.RUnlock()
	this.bufLock.Unlock()

	return n, err
}

//UnreadBytes returns the number of bytes that have yet to be read on the calling *RingBuffer
func (this *RingBuffer) UnreadBytes() int {
	this.pendingWriteLock.Lock()
	defer this.pendingWriteLock.Unlock()
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	return this.diff
}

//WritableBytes returns the number of free bytes that are able to be written into the called *RingBuffer.
func (this *RingBuffer) WritableBytes() int {
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	return this.maxSize - this.diff
}

func (this *RingBuffer) isSafeToWrite(len int) bool {
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	return this.diff+len <= this.maxSize
}

func (this *RingBuffer) isSafeToRead(len int) bool {
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	this.doneLock.RLock()
	defer this.doneLock.RUnlock()
	return this.done || this.diff >= len
}

//WriteIsComplete signals that no more data will be written into the calling *RingBuffer, and that any remaining data is expected to be complete and safe to read.
func (this *RingBuffer) WriteIsComplete() {
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	this.doneLock.Lock()
	this.done = true
	this.doneLock.Unlock()
	this.readLock.Signal()
}

//Reset sets the internal `diff` to 0 and reopens the calling *RingBuffer for writing.  Any readable bytes, while still in the buffer, will not be readable via Read, and will be marked as writable bytes.
func (this *RingBuffer) Reset() {
	this.bufLock.Lock()
	defer this.bufLock.Unlock()
	this.diff = 0
	this.doneLock.Lock()
	this.done = false
	this.doneLock.Unlock()
}
