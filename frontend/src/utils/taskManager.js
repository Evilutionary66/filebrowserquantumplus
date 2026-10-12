import { reactive } from "vue";
import i18n from "@/i18n/index.ts";
import { notify } from "@/notify/index.ts";
import { mutations } from "@/store/index.ts";
import { getTask, listTasks, cancelTask } from "@/api/tasks.js";
import {
  notifyMoveCopyComplete,
  notifyOperationError,
} from "@/utils/appNotifications.js";

const POLL_INTERVAL_MS = 1000;
// How long a finished task stays in the panel before being auto-dismissed.
const AUTO_DISMISS_MS = 10000;
const AUTO_DISMISS_ERROR_MS = 30000;
// Sliding window used to compute the live transfer speed (bytes/sec).
const SPEED_WINDOW_MS = 5000;
const TERMINAL_STATUSES = ["completed", "partial", "failed", "cancelled"];

class TaskManager {
  constructor() {
    // Tracked tasks, mirrored from the backend task snapshots. Reactive so the
    // progress panel re-renders as snapshots update.
    this.tracked = reactive([]);
    this._timers = new Map();
    this._dismissTimers = new Map();
    this._recovered = false;
  }

  /** Start tracking a task returned by a 202 response ({ taskId }). */
  track(taskId) {
    if (!taskId || this.findById(taskId)) {
      return;
    }
    const entry = {
      id: taskId,
      action: "copy",
      status: "queued",
      totalItems: 0,
      doneItems: 0,
      totalBytes: 0,
      doneBytes: 0,
      speed: 0,
      samples: [],
      createdAt: Date.now(),
      startedAt: null,
      finishedAt: null,
      items: [],
      message: "",
    };
    this.tracked.push(entry);
    this._startPolling(taskId);
    // Fetch the first snapshot immediately so the panel shows real totals.
    void this._poll(taskId);
  }

  /** Re-attach to tasks that are still running on the server (page reload). */
  async recover() {
    if (this._recovered) {
      return;
    }
    this._recovered = true;
    try {
      const data = await listTasks();
      for (const task of data?.tasks || []) {
        if (TERMINAL_STATUSES.includes(task.status)) {
          continue;
        }
        this.track(task.id);
      }
    } catch {
      // Server may not support task endpoints (older backend) — ignore.
    }
  }

  findById(id) {
    return this.tracked.find((item) => item.id === id);
  }

  hasActive() {
    return this.tracked.some((item) => !TERMINAL_STATUSES.includes(item.status));
  }

  async cancel(id) {
    const entry = this.findById(id);
    if (!entry || TERMINAL_STATUSES.includes(entry.status)) {
      return;
    }
    try {
      const snapshot = await cancelTask(id);
      this._applySnapshot(entry, snapshot);
    } catch (err) {
      if (err.status === 409) {
        // Already finished server-side; the next poll will pick up the real status.
        return;
      }
      notify.showError(err.message || i18n.global.t("tasks.cancelFailed"));
    }
  }

  dismiss(id) {
    this._stopPolling(id);
    this._clearDismissTimer(id);
    const index = this.tracked.findIndex((item) => item.id === id);
    if (index !== -1) {
      this.tracked.splice(index, 1);
    }
  }

  dismissFinished() {
    for (const item of [...this.tracked]) {
      if (TERMINAL_STATUSES.includes(item.status)) {
        this.dismiss(item.id);
      }
    }
  }

  _startPolling(id) {
    if (this._timers.has(id)) {
      return;
    }
    const timer = setInterval(() => this._poll(id), POLL_INTERVAL_MS);
    this._timers.set(id, timer);
  }

  _stopPolling(id) {
    const timer = this._timers.get(id);
    if (timer) {
      clearInterval(timer);
      this._timers.delete(id);
    }
  }

  _clearDismissTimer(id) {
    const timer = this._dismissTimers.get(id);
    if (timer) {
      clearTimeout(timer);
      this._dismissTimers.delete(id);
    }
  }

  async _poll(id) {
    const entry = this.findById(id);
    if (!entry) {
      this._stopPolling(id);
      return;
    }
    try {
      const snapshot = await getTask(id);
      this._applySnapshot(entry, snapshot);
    } catch (err) {
      if (err.status === 404) {
        // Task forgotten by the server (restart or pruning) — drop it.
        this.dismiss(id);
        return;
      }
      // Transient network error: keep polling, the next tick may succeed.
    }
  }

  _applySnapshot(entry, snapshot) {
    if (!snapshot) {
      return;
    }
    entry.action = snapshot.action || entry.action;
    entry.status = snapshot.status || entry.status;
    entry.totalItems = snapshot.totalItems ?? entry.totalItems;
    entry.doneItems = snapshot.doneItems ?? entry.doneItems;
    entry.totalBytes = snapshot.totalBytes ?? entry.totalBytes;
    entry.doneBytes = snapshot.doneBytes ?? entry.doneBytes;
    entry.createdAt = snapshot.createdAt ?? entry.createdAt;
    entry.startedAt = snapshot.startedAt ?? entry.startedAt;
    entry.finishedAt = snapshot.finishedAt ?? entry.finishedAt;
    entry.items = snapshot.items || entry.items;
    this._updateSpeed(entry);

    if (TERMINAL_STATUSES.includes(entry.status)) {
      this._stopPolling(entry.id);
      this._onTaskFinished(entry);
    }
  }

  /**
   * Maintain a short sliding window of progress samples and derive the live
   * transfer rate. Byte-based tasks report bytes/sec; item-based tasks
   * (same-volume moves, renames, deletes) report items/sec.
   */
  _updateSpeed(entry) {
    const now = Date.now();
    const byBytes = entry.totalBytes > 0;
    if (!entry.samples) {
      entry.samples = [];
    }
    const samples = entry.samples;
    samples.push({
      t: now,
      value: byBytes ? entry.doneBytes : entry.doneItems,
    });
    while (samples.length > 1 && now - samples[0].t > SPEED_WINDOW_MS) {
      samples.shift();
    }

    if (TERMINAL_STATUSES.includes(entry.status)) {
      // Finished: show the average rate over the whole run instead of the
      // instantaneous window rate.
      const start = entry.startedAt ? Date.parse(entry.startedAt) : entry.createdAt;
      const end = entry.finishedAt ? Date.parse(entry.finishedAt) : now;
      const secs = (end - start) / 1000;
      entry.speed = secs > 0 ? (byBytes ? entry.doneBytes : entry.doneItems) / secs : 0;
      entry.speedUnit = byBytes ? "bytes" : "items";
      return;
    }

    if (samples.length < 2) {
      entry.speed = 0;
      entry.speedUnit = byBytes ? "bytes" : "items";
      return;
    }
    const first = samples[0];
    const last = samples[samples.length - 1];
    const secs = (last.t - first.t) / 1000;
    const rate = secs > 0 ? (last.value - first.value) / secs : 0;
    entry.speed = Math.max(0, rate);
    entry.speedUnit = byBytes ? "bytes" : "items";
  }

  _onTaskFinished(entry) {
    // Refresh file listings so completed changes are visible.
    mutations.setReload(true);

    const t = i18n.global.t;
    const label = t(`tasks.${entry.action}`);
    const failedItems = (entry.items || []).filter((i) => i.status === "failed");

    if (entry.status === "completed") {
      notify.showSuccessToast(t("tasks.completed", { action: label }));
      if (entry.action === "copy" || entry.action === "move") {
        notifyMoveCopyComplete(entry.action, entry.totalItems || entry.doneItems);
      }
    } else if (entry.status === "partial") {
      notify.showWarningToast(
        t("tasks.partial", {
          action: label,
          succeeded: entry.doneItems,
          failed: failedItems.length,
        })
      );
      notifyOperationError(t("tasks.partial", {
        action: label,
        succeeded: entry.doneItems,
        failed: failedItems.length,
      }));
    } else if (entry.status === "cancelled") {
      notify.showInfoToast(t("tasks.cancelled", { action: label }));
    } else {
      const message = failedItems[0]?.message || t("prompts.operationFailed");
      notify.showErrorToast(t("tasks.failed", { action: label }));
      notifyOperationError(message);
    }

    const ttl =
      entry.status === "completed" || entry.status === "cancelled"
        ? AUTO_DISMISS_MS
        : AUTO_DISMISS_ERROR_MS;
    this._clearDismissTimer(entry.id);
    this._dismissTimers.set(
      entry.id,
      setTimeout(() => this.dismiss(entry.id), ttl)
    );
  }
}

export const taskManager = new TaskManager();
