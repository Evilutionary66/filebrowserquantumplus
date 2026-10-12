package fileutils

import (
	"errors"
	"sync/atomic"
)

// copyChunkSize is the buffer size used when copying with progress reporting.
const copyChunkSize = 1024 * 1024

// ErrCopyCancelled is returned when a copy/move operation is cancelled by the
// user through a CopyHook between chunks or items.
var ErrCopyCancelled = errors.New("operation cancelled")

// CopyHook carries progress and cancellation state for copy/move operations.
// It is safe for concurrent use. A nil *CopyHook means "no progress tracking",
// which keeps the fast io.Copy path for regular callers.
type CopyHook struct {
	cancelled atomic.Bool
	// OnProgress is called with the number of bytes just copied. It may return
	// false to request cancellation; the copy then aborts with ErrCopyCancelled.
	OnProgress func(bytes int64) bool
	// OnItemDone is called each time an individual file inside a directory
	// tree finishes copying.
	OnItemDone func()
}

// AddBytes reports copied bytes and returns false when the operation should stop.
func (h *CopyHook) AddBytes(n int64) bool {
	if h == nil {
		return true
	}
	if h.cancelled.Load() {
		return false
	}
	if h.OnProgress == nil {
		return true
	}
	return h.OnProgress(n)
}

// ItemDone notifies the hook that one file finished.
func (h *CopyHook) ItemDone() {
	if h == nil || h.OnItemDone == nil {
		return
	}
	h.OnItemDone()
}

// Cancel requests cooperative cancellation of the operation.
func (h *CopyHook) Cancel() {
	if h == nil {
		return
	}
	h.cancelled.Store(true)
}

// IsCancelled reports whether cancellation was requested.
func (h *CopyHook) IsCancelled() bool {
	return h != nil && h.cancelled.Load()
}
