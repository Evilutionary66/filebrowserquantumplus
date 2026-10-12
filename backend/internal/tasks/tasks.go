// Package tasks provides an in-memory registry of long-running file operations
// (copy/move/rename/delete) so the HTTP handlers can return immediately and the
// frontend can poll for progress instead of blocking on a spinner.
package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
)

// Task lifecycle statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusPartial   = "partial"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Item statuses inside a task.
const (
	ItemPending   = "pending"
	ItemRunning   = "running"
	ItemDone      = "done"
	ItemFailed    = "failed"
	ItemCancelled = "cancelled"
)

// maxRetained limits how many finished tasks stay in memory.
const maxRetained = 50

// Item tracks the status of one file/directory inside a task.
type Item struct {
	FromSource string `json:"fromSource,omitempty"`
	FromPath   string `json:"fromPath,omitempty"`
	ToSource   string `json:"toSource,omitempty"`
	ToPath     string `json:"toPath,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// Task is a single user-initiated batch operation.
type Task struct {
	ID         string    `json:"id"`
	Username   string    `json:"-"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	TotalItems int       `json:"totalItems"`
	DoneItems  int       `json:"doneItems"`
	TotalBytes int64     `json:"totalBytes"`
	DoneBytes  int64     `json:"doneBytes"`
	CreatedAt  time.Time `json:"createdAt"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Items      []Item    `json:"items"`

	mu              *sync.Mutex
	cancelRequested bool
	hook            *fileutils.CopyHook
}

// New registers a new queued task and returns it.
func New(username, action string, items []Item, totalBytes int64) *Task {
	t := &Task{
		ID:         newID(),
		Username:   username,
		Action:     action,
		Status:     StatusQueued,
		TotalItems: len(items),
		TotalBytes: totalBytes,
		CreatedAt:  time.Now(),
		Items:      items,
		mu:         &sync.Mutex{},
	}
	t.hook = &fileutils.CopyHook{
		OnProgress: func(n int64) bool {
			t.mu.Lock()
			cancel := t.cancelRequested
			t.mu.Unlock()
			if cancel {
				return false
			}
			t.mu.Lock()
			t.DoneBytes += n
			t.mu.Unlock()
			return true
		},
	}
	registryMu.Lock()
	taskByID[t.ID] = t
	order = append(order, t.ID)
	pruneLocked()
	registryMu.Unlock()
	return t
}

// Hook returns the progress/cancellation hook to pass into file operations.
func (t *Task) Hook() *fileutils.CopyHook {
	return t.hook
}

// Begin transitions the task to running.
func (t *Task) Begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = StatusRunning
	t.StartedAt = time.Now()
}

// CancelRequested reports whether cancellation was requested.
func (t *Task) CancelRequested() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelRequested
}

// Cancel requests cooperative cancellation. Returns false when the task has
// already finished.
func (t *Task) Cancel() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status == StatusCompleted || t.Status == StatusPartial ||
		t.Status == StatusFailed || t.Status == StatusCancelled {
		return false
	}
	t.cancelRequested = true
	if t.hook != nil {
		t.hook.Cancel()
	}
	return true
}

// MarkItemRunning marks item i as running.
func (t *Task) MarkItemRunning(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < len(t.Items) {
		t.Items[i].Status = ItemRunning
	}
}

// MarkItemDone marks item i as completed and increments progress counters.
func (t *Task) MarkItemDone(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < len(t.Items) {
		t.Items[i].Status = ItemDone
	}
	t.DoneItems++
}

// MarkItemFailed marks item i as failed and increments progress counters.
func (t *Task) MarkItemFailed(i int, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < len(t.Items) {
		t.Items[i].Status = ItemFailed
		t.Items[i].Message = message
	}
	t.DoneItems++
}

// MarkItemCancelled marks item i as cancelled and increments progress counters.
func (t *Task) MarkItemCancelled(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < len(t.Items) {
		t.Items[i].Status = ItemCancelled
	}
	t.DoneItems++
}

// Finish computes the final status from item results and stamps the finish time.
func (t *Task) Finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	var done, failed, cancelled int
	for _, it := range t.Items {
		switch it.Status {
		case ItemDone:
			done++
		case ItemFailed:
			failed++
		case ItemCancelled:
			cancelled++
		}
	}
	switch {
	case done == len(t.Items):
		t.Status = StatusCompleted
	case done == 0 && cancelled > 0 && failed == 0:
		t.Status = StatusCancelled
	case done == 0 && failed > 0:
		t.Status = StatusFailed
	case failed+cancelled > 0:
		t.Status = StatusPartial
	default:
		t.Status = StatusCompleted
	}
	t.FinishedAt = time.Now()
}

// Snapshot returns a JSON-safe copy safe to serialize without holding locks.
func (t *Task) Snapshot() *Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := *t
	snap.mu = &sync.Mutex{}
	snap.hook = nil
	snap.Items = make([]Item, len(t.Items))
	copy(snap.Items, t.Items)
	return &snap
}

func (t *Task) isFinished() bool {
	switch t.Status {
	case StatusCompleted, StatusPartial, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

var (
	registryMu sync.Mutex
	taskByID   = map[string]*Task{}
	order      []string
)

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// Get returns the task with the given id, or nil.
func Get(id string) *Task {
	registryMu.Lock()
	defer registryMu.Unlock()
	return taskByID[id]
}

// List returns snapshots of all tasks owned by username, newest first.
func List(username string) []*Task {
	registryMu.Lock()
	defer registryMu.Unlock()
	var out []*Task
	for i := len(order) - 1; i >= 0; i-- {
		t := taskByID[order[i]]
		if t == nil || t.Username != username {
			continue
		}
		out = append(out, t.Snapshot())
	}
	return out
}

// HasRunning reports whether the user currently has any queued/running task.
func HasRunning(username string) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, t := range taskByID {
		if t.Username != username {
			continue
		}
		t.mu.Lock()
		finished := t.isFinished()
		t.mu.Unlock()
		if !finished {
			return true
		}
	}
	return false
}

// pruneLocked drops the oldest finished tasks beyond maxRetained.
// registryMu must be held.
func pruneLocked() {
	if len(order) <= maxRetained {
		return
	}
	kept := make([]string, 0, len(order))
	removed := 0
	for _, id := range order {
		t := taskByID[id]
		if t != nil {
			t.mu.Lock()
			finished := t.isFinished()
			t.mu.Unlock()
			if finished && removed < len(order)-maxRetained {
				delete(taskByID, id)
				removed++
				continue
			}
		}
		kept = append(kept, id)
	}
	order = kept
}

// SyncBytes raises DoneBytes to at least minimum. Used for operations that
// complete per item without byte-level reporting (rename, delete, same-volume
// move) so the progress bar still advances as items finish.
func (t *Task) SyncBytes(minimum int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.DoneBytes < minimum {
		t.DoneBytes = minimum
	}
}
