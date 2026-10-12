package tasks

import (
	"fmt"
	"testing"
)

func newTestItems(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{FromPath: fmt.Sprintf("/from/%d", i), ToPath: fmt.Sprintf("/to/%d", i)}
	}
	return items
}

func TestNewRegistersTask(t *testing.T) {
	t.Parallel()
	items := newTestItems(3)
	task := New("alice", "copy", items, 1000)
	if task.ID == "" {
		t.Fatal("expected non-empty task id")
	}
	if task.Status != StatusQueued {
		t.Fatalf("expected status %q, got %q", StatusQueued, task.Status)
	}
	if task.TotalItems != 3 || task.TotalBytes != 1000 {
		t.Fatalf("unexpected totals: items=%d bytes=%d", task.TotalItems, task.TotalBytes)
	}
	if Get(task.ID) != task {
		t.Fatal("Get should return the registered task")
	}
	if task.Hook() == nil {
		t.Fatal("Hook should be non-nil")
	}
}

func TestListFiltersByOwnerAndIsNewestFirst(t *testing.T) {
	t.Parallel()
	older := New("bob", "copy", newTestItems(1), 0)
	newer := New("bob", "move", newTestItems(1), 0)
	other := New("carol", "copy", newTestItems(1), 0)

	list := List("bob")
	if len(list) != 2 {
		t.Fatalf("expected 2 tasks for bob, got %d", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != older.ID {
		t.Fatal("expected newest-first ordering")
	}
	for _, snap := range list {
		if snap.hook != nil {
			t.Fatal("snapshot must not carry the copy hook")
		}
	}
	listOther := List("carol")
	if len(listOther) != 1 || listOther[0].ID != other.ID {
		t.Fatal("expected only carol's task in her list")
	}
	if len(List("dave")) != 0 {
		t.Fatal("expected empty list for unknown user")
	}
}

func TestFinishStatusMatrix(t *testing.T) {
	tests := []struct {
		name   string
		status []string
		want   string
	}{
		{"all done", []string{ItemDone, ItemDone, ItemDone}, StatusCompleted},
		{"all cancelled", []string{ItemCancelled, ItemCancelled}, StatusCancelled},
		{"all failed", []string{ItemFailed, ItemFailed}, StatusFailed},
		{"done and failed", []string{ItemDone, ItemFailed}, StatusPartial},
		{"done and cancelled", []string{ItemDone, ItemCancelled}, StatusPartial},
		{"failed and cancelled", []string{ItemFailed, ItemCancelled}, StatusFailed},
		{"empty task", []string{}, StatusCompleted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task := New("user", "copy", newTestItems(len(tc.status)), 0)
			for i, st := range tc.status {
				switch st {
				case ItemDone:
					task.MarkItemDone(i)
				case ItemFailed:
					task.MarkItemFailed(i, "boom")
				case ItemCancelled:
					task.MarkItemCancelled(i)
				}
			}
			task.Finish()
			if task.Status != tc.want {
				t.Fatalf("expected status %q, got %q", tc.want, task.Status)
			}
			if task.FinishedAt.IsZero() {
				t.Fatal("FinishedAt should be stamped")
			}
			if task.DoneItems != len(tc.status) {
				t.Fatalf("expected DoneItems=%d, got %d", len(tc.status), task.DoneItems)
			}
		})
	}
}

func TestMarkItemStatuses(t *testing.T) {
	t.Parallel()
	task := New("user", "move", newTestItems(2), 0)
	task.MarkItemRunning(0)
	if task.Items[0].Status != ItemRunning {
		t.Fatalf("expected %q, got %q", ItemRunning, task.Items[0].Status)
	}
	task.MarkItemFailed(0, "kaboom")
	if task.Items[0].Status != ItemFailed || task.Items[0].Message != "kaboom" {
		t.Fatalf("failed item not recorded: %+v", task.Items[0])
	}
	task.MarkItemDone(1)
	if task.Items[1].Status != ItemDone {
		t.Fatalf("expected %q, got %q", ItemDone, task.Items[1].Status)
	}
	// Out-of-range indexes must not panic; they still count toward progress.
	task.MarkItemRunning(5)
	task.MarkItemDone(5)
	if task.DoneItems != 3 {
		t.Fatalf("out-of-range marks should still count progress, got %d", task.DoneItems)
	}
}

func TestCancelCooperative(t *testing.T) {
	t.Parallel()
	task := New("user", "copy", newTestItems(1), 100)
	if task.CancelRequested() {
		t.Fatal("fresh task should not have cancellation requested")
	}
	// Hook reports progress: calls accumulate bytes until cancel is requested.
	if !task.Hook().OnProgress(40) {
		t.Fatal("hook should accept progress before cancellation")
	}
	if task.DoneBytes != 40 {
		t.Fatalf("expected DoneBytes=40, got %d", task.DoneBytes)
	}
	if !task.Cancel() {
		t.Fatal("Cancel should succeed on a running task")
	}
	if !task.CancelRequested() {
		t.Fatal("CancelRequested should be true after Cancel")
	}
	if task.Hook().OnProgress(10) {
		t.Fatal("hook should stop after cancellation was requested")
	}
	if task.DoneBytes != 40 {
		t.Fatalf("DoneBytes should stay at 40 after cancel, got %d", task.DoneBytes)
	}
	// Finished tasks cannot be cancelled.
	task.Finish()
	if task.Cancel() {
		t.Fatal("Cancel should return false for a finished task")
	}
}

func TestHasRunning(t *testing.T) {
	t.Parallel()
	running := New("erin", "copy", newTestItems(1), 0)
	if !HasRunning("erin") {
		t.Fatal("expected HasRunning true for a queued task")
	}
	running.Finish()
	if HasRunning("erin") {
		t.Fatal("expected HasRunning false after finish")
	}
	if HasRunning("nobody") {
		t.Fatal("expected HasRunning false for unknown user")
	}
}

func TestPruneKeepsMaxRetainedFinished(t *testing.T) {
	t.Parallel()
	// Create more than maxRetained finished tasks under a unique owner.
	var ids []string
	for i := 0; i < maxRetained+5; i++ {
		task := New(fmt.Sprintf("prune-user-%d", 0), "delete", newTestItems(1), 0)
		task.Finish()
		ids = append(ids, task.ID)
	}
	// The oldest ones beyond the limit should have been pruned from the index.
	remaining := 0
	for _, id := range ids {
		if Get(id) != nil {
			remaining++
		}
	}
	if remaining > maxRetained {
		t.Fatalf("expected at most %d retained tasks, got %d", maxRetained, remaining)
	}
	// Newest ones must survive.
	if Get(ids[len(ids)-1]) == nil {
		t.Fatal("newest task should never be pruned")
	}
}

func TestPruneKeepsUnfinishedTasks(t *testing.T) {
	t.Parallel()
	// Fill the registry with finished tasks, then add an unfinished one at the
	// oldest position by finishing everything else afterwards.
	var tasksList []*Task
	for i := 0; i < maxRetained+5; i++ {
		tasksList = append(tasksList, New("mixed-user", "copy", newTestItems(1), 0))
	}
	// Only the first task stays unfinished.
	for _, task := range tasksList[1:] {
		task.Finish()
	}
	if Get(tasksList[0].ID) == nil {
		t.Fatal("unfinished task must not be pruned even when oldest")
	}
}

func TestSnapshotIsolation(t *testing.T) {
	t.Parallel()
	task := New("user", "copy", newTestItems(2), 100)
	snap := task.Snapshot()
	task.MarkItemDone(0)
	task.DoneBytes = 999
	if snap.DoneItems != 0 {
		t.Fatal("snapshot must not observe later mutations to counters")
	}
	if snap.Items[0].Status == ItemDone {
		t.Fatal("snapshot must deep-copy items")
	}
	if snap.DoneBytes != 0 {
		t.Fatal("snapshot must not observe later byte updates")
	}
	if snap.ID != task.ID || snap.Action != task.Action || snap.Status != task.Status {
		t.Fatal("snapshot should copy scalar fields")
	}
}

func TestSyncBytes(t *testing.T) {
	t.Parallel()
	task := New("user", "rename", newTestItems(1), 0)
	task.SyncBytes(500)
	if task.DoneBytes != 500 {
		t.Fatalf("expected DoneBytes=500, got %d", task.DoneBytes)
	}
	// SyncBytes only raises, never lowers.
	task.SyncBytes(100)
	if task.DoneBytes != 500 {
		t.Fatalf("SyncBytes must not lower DoneBytes, got %d", task.DoneBytes)
	}
}

func TestBeginStampsRunning(t *testing.T) {
	t.Parallel()
	task := New("user", "copy", newTestItems(1), 0)
	task.Begin()
	if task.Status != StatusRunning {
		t.Fatalf("expected status %q, got %q", StatusRunning, task.Status)
	}
	if task.StartedAt.IsZero() {
		t.Fatal("Begin should stamp StartedAt")
	}
}
