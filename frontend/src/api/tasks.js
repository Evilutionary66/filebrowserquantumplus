// API client for background task endpoints (/api/tasks/*).
// Tasks are created implicitly by the server when a bulk copy/move/delete
// operation is large enough to run asynchronously (HTTP 202 + taskId).

import { getApiPath } from "@/utils/url.js";
import { fetchURL } from "./utils.ts";

export async function listTasks() {
  const res = await fetchURL(getApiPath("tasks"));
  return res.json();
}

export async function getTask(id) {
  const res = await fetchURL(getApiPath(`tasks/${id}`));
  return res.json();
}

export async function cancelTask(id) {
  const res = await fetchURL(getApiPath(`tasks/${id}/cancel`), {
    method: "POST",
  });
  return res.json();
}
