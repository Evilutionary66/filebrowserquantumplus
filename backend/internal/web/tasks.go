package web

import (
	"fmt"
	"net/http"

	"github.com/gtsteffaniak/filebrowser/backend/internal/tasks"
)

// tasksListHandler returns all tasks owned by the current user, newest first.
// @Summary List background tasks
// @Description Returns the in-memory task list (copy/move/rename/delete operations running in the background) for the authenticated user, newest first.
// @Tags Tasks
// @Produce json
// @Success 200 {object} map[string]interface{} "List of tasks"
// @Router /api/tasks [get]
func tasksListHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	list := tasks.List(d.User.Username)
	if list == nil {
		list = []*tasks.Task{}
	}
	return RenderJSON(w, r, map[string]interface{}{"tasks": list})
}

// taskGetHandler returns a single task by id.
// @Summary Get a background task
// @Description Returns the status/progress of a single task owned by the authenticated user.
// @Tags Tasks
// @Produce json
// @Param id path string true "Task id"
// @Success 200 {object} tasks.Task "Task snapshot"
// @Failure 404 {object} map[string]string "Task not found"
// @Router /api/tasks/{id} [get]
func taskGetHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	t := getOwnedTask(w, r, d)
	if t == nil {
		return http.StatusNotFound, fmt.Errorf("task not found")
	}
	return RenderJSON(w, r, t.Snapshot())
}

// taskCancelHandler requests cooperative cancellation of a running task.
// @Summary Cancel a background task
// @Description Requests cancellation of a queued or running task. Already-transferred work is kept.
// @Tags Tasks
// @Produce json
// @Param id path string true "Task id"
// @Success 200 {object} tasks.Task "Task snapshot"
// @Failure 404 {object} map[string]string "Task not found"
// @Failure 409 {object} map[string]string "Task already finished"
// @Router /api/tasks/{id}/cancel [post]
func taskCancelHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	t := getOwnedTask(w, r, d)
	if t == nil {
		return http.StatusNotFound, fmt.Errorf("task not found")
	}
	if !t.Cancel() {
		return http.StatusConflict, fmt.Errorf("task already finished")
	}
	return RenderJSON(w, r, t.Snapshot())
}

// getOwnedTask resolves a task by path id and verifies ownership.
func getOwnedTask(w http.ResponseWriter, r *http.Request, d *Context) *tasks.Task {
	t := tasks.Get(r.PathValue("id"))
	if t == nil || t.Username != d.User.Username {
		return nil
	}
	return t
}
