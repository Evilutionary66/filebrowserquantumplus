<template>
  <transition name="task-panel">
    <div v-if="tasks.length > 0" class="task-panel" role="region" :aria-label="$t('tasks.title')">
      <div class="task-panel-header">
        <span class="task-panel-title">
          <i class="material-symbols">pending</i>
          {{ $t("tasks.title") }}
        </span>
        <div class="task-panel-actions">
          <button
            v-if="hasFinished"
            type="button"
            class="panel-action"
            :aria-label="$t('tasks.clearFinished')"
            :title="$t('tasks.clearFinished')"
            @click="taskManager.dismissFinished()"
          >
            <i class="material-symbols">done_all</i>
          </button>
          <button
            type="button"
            class="panel-action"
            :aria-label="collapsed ? $t('tasks.expand') : $t('tasks.collapse')"
            :title="collapsed ? $t('tasks.expand') : $t('tasks.collapse')"
            @click="collapsed = !collapsed"
          >
            <i class="material-symbols">{{ collapsed ? "expand_more" : "expand_less" }}</i>
          </button>
        </div>
      </div>
      <div v-if="!collapsed" class="task-list">
        <div
          v-for="task in tasks"
          :key="task.id"
          class="task-item"
          :class="`task-item--${task.status}`"
        >
          <i class="material-symbols task-icon">{{ actionIcon(task.action) }}</i>
          <div class="task-info">
            <div class="task-head">
              <p class="task-title">
                {{ statusLabel(task) }}
                <span v-if="task.totalItems > 1" class="task-count">
                  ({{ task.doneItems }}/{{ task.totalItems }})
                </span>
              </p>
              <span v-if="speedText(task)" class="task-speed">{{ speedText(task) }}</span>
            </div>
            <progress-bar
              :val="progressVal(task)"
              :unit="task.totalBytes > 0 ? 'bytes' : ''"
              :max="progressMax(task)"
              :status="barStatus(task)"
              text-position="inside"
              size="20"
            ></progress-bar>
            <p v-if="failureReason(task)" class="task-error" :title="failureReason(task)">
              {{ failureReason(task) }}
            </p>
          </div>
          <div class="task-actions">
            <button
              v-if="isActive(task)"
              type="button"
              class="action"
              :aria-label="$t('tasks.cancel')"
              :title="$t('tasks.cancel')"
              @click="taskManager.cancel(task.id)"
            >
              <i class="material-symbols">close</i>
            </button>
            <button
              v-else
              type="button"
              class="action"
              :aria-label="$t('tasks.dismiss')"
              :title="$t('tasks.dismiss')"
              @click="taskManager.dismiss(task.id)"
            >
              <i class="material-symbols">close</i>
            </button>
          </div>
        </div>
      </div>
    </div>
  </transition>
</template>

<script>
import { taskManager } from "@/utils/taskManager.js";
import { getHumanReadableFilesize } from "@/utils/filesizes.js";
import ProgressBar from "@/components/ProgressBar.vue";

const ACTION_ICONS = {
  copy: "content_copy",
  move: "drive_file_move_rtl",
  delete: "delete",
  archive: "folder_zip",
  unarchive: "unarchive",
};

export default {
  name: "TaskProgress",
  components: {
    ProgressBar,
  },
  data: () => ({
    collapsed: false,
    taskManager: taskManager,
  }),
  computed: {
    tasks() {
      return taskManager.tracked;
    },
    hasFinished() {
      return this.tasks.some((t) => !this.isActive(t));
    },
  },
  mounted() {
    // Re-attach to tasks still running on the server after a page reload.
    taskManager.recover();
  },
  methods: {
    isActive(task) {
      return task.status === "queued" || task.status === "running";
    },
    actionIcon(action) {
      return ACTION_ICONS[action] || "pending";
    },
    statusLabel(task) {
      if (task.status === "completed") {
        return this.$t("tasks.completed", { action: this.$t(`tasks.${task.action}`) });
      }
      if (task.status === "partial") {
        return this.$t("tasks.partialShort", { action: this.$t(`tasks.${task.action}`) });
      }
      if (task.status === "failed") {
        return this.$t("tasks.failedShort", { action: this.$t(`tasks.${task.action}`) });
      }
      if (task.status === "cancelled") {
        return this.$t("tasks.cancelled", { action: this.$t(`tasks.${task.action}`) });
      }
      if (task.status === "queued") {
        return this.$t("tasks.queued", { action: this.$t(`tasks.${task.action}`) });
      }
      return this.$t(`tasks.${task.action}`);
    },
    failureReason(task) {
      if (task.status !== "failed" && task.status !== "partial") {
        return "";
      }
      const failed = (task.items || []).filter((i) => i.status === "failed" && i.message);
      return failed.length > 0 ? failed[0].message : "";
    },
    progressVal(task) {
      if (task.totalBytes > 0) {
        return task.doneBytes;
      }
      if (task.status === "completed") {
        return this.$t("tasks.done");
      }
      return task.doneItems;
    },
    progressMax(task) {
      return task.totalBytes > 0 ? task.totalBytes : task.totalItems;
    },
    barStatus(task) {
      switch (task.status) {
        case "failed":
          return "error";
        case "partial":
          return "conflict";
        case "cancelled":
          return "disk";
        case "queued":
          return "indexing";
        default:
          return "default";
      }
    },
    speedText(task) {
      if (!task.speed || task.speed <= 0) {
        return "";
      }
      if (task.speedUnit === "items") {
        return this.$t("tasks.speedItems", { speed: Math.round(task.speed) });
      }
      return this.$t("tasks.speedBytes", { speed: `${getHumanReadableFilesize(task.speed)}/s` });
    },
  },
};
</script>

<style scoped>
.task-panel {
  position: fixed;
  bottom: 4em;
  right: 1em;
  z-index: 9000;
  width: 340px;
  max-width: calc(100vw - 2em);
  background: var(--surfacePrimary);
  border: 1px solid var(--divider);
  border-radius: var(--borderRadius);
  box-shadow: var(--surfaceElevationShadow);
  color: var(--textPrimary);
  overflow: hidden;
}

.task-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.6em 0.9em;
  background: var(--surfaceSecondary);
  font-weight: 600;
  font-size: 0.9em;
}

.task-panel-title {
  display: flex;
  align-items: center;
  gap: 0.4em;
}

.task-panel-title .material-symbols {
  font-size: 1.1em;
  color: var(--primaryColor);
}

.task-panel-actions {
  display: flex;
  align-items: center;
  gap: 0.25em;
}

.panel-action {
  background: transparent;
  border: none;
  cursor: pointer;
  color: var(--textPrimary);
  padding: 0.2em;
  border-radius: var(--borderRadius);
  display: flex;
  align-items: center;
}

.panel-action:hover {
  background: var(--hoverOverlay);
}

.panel-action .material-symbols {
  font-size: 1.2em;
}

.task-list {
  max-height: 40vh;
  overflow-y: auto;
  padding: 0.25em 0;
}

.task-item {
  display: flex;
  align-items: center;
  gap: 0.75em;
  padding: 0.75em 0.9em;
}

.task-item + .task-item {
  border-top: 1px solid var(--divider);
}

.task-icon {
  font-size: 1.3em;
  flex-shrink: 0;
  color: var(--primaryColor);
}

.task-item--failed .task-icon {
  color: var(--red);
}

.task-item--partial .task-icon {
  color: var(--icon-orange);
}

.task-item--cancelled .task-icon {
  color: var(--textSecondary);
}

.task-info {
  flex: 1;
  min-width: 0;
}

.task-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.5em;
}

.task-title {
  margin: 0 0 0.4em;
  font-size: 0.85em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.task-count {
  color: var(--textSecondary);
}

.task-speed {
  flex-shrink: 0;
  font-size: 0.75em;
  font-variant-numeric: tabular-nums;
  color: var(--textSecondary);
}

.task-error {
  margin: 0.35em 0 0;
  font-size: 0.75em;
  color: var(--red);
  overflow-wrap: anywhere;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.task-actions {
  flex-shrink: 0;
}

.task-actions .action {
  background: transparent;
  border: none;
  cursor: pointer;
  color: var(--textPrimary);
  padding: 0.25em;
  border-radius: var(--borderRadius);
  display: flex;
  align-items: center;
}

.task-actions .action:hover {
  background: var(--hoverOverlay);
}

/* Animations */
.task-panel-enter-active {
  animation: task-panel-in 0.25s ease-out;
}

.task-panel-leave-active {
  animation: task-panel-out 0.2s ease-in;
}

@keyframes task-panel-in {
  from {
    opacity: 0;
    transform: translateY(1em);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@keyframes task-panel-out {
  from {
    opacity: 1;
    transform: translateY(0);
  }
  to {
    opacity: 0;
    transform: translateY(1em);
  }
}

@media (width <= 768px) {
  .task-panel {
    right: 0.5em;
    bottom: 3.5em;
    width: calc(100vw - 1em);
  }
}
</style>
