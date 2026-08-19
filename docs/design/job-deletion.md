# Background Job Deletion in Web UI & API

Status: **Approved**

## Context & Motivation

Background jobs (`JobStore`) capture commands run in the background (via `bash(background: true)` or manual start in the UI).
Previously, users could stop running jobs and view logs, but had no mechanism to delete completed, failed, killed, or running jobs from the session history.

This design introduces job deletion support across the backend store, HTTP API, `jobstrip-line` (the composer-docked strip), and the Manage modal.

## Goals

1. **Clean job disposal**: Allow users to delete any job (running, completed, failed, or killed) directly from the `jobstrip-line` actions.
2. **Safe cancellation on delete**: If a running job is deleted, kill its underlying process/group before removing it from store memory and persistence (`.moony/jobs.json`).
3. **Immediate reactivity**: Broadcast updated jobs state over SSE so all UI views (and sessions) update instantly without stale state or dangling log tailing.
4. **Consistency across web views**: Support deletion in both `jobstrip-line` (via a trash icon button) and the Manage Background Jobs modal (via a Delete button).
5. **Robust API**: Support `DELETE /api/sessions/{id}/jobs/{job}` as well as `POST /api/sessions/{id}/jobs/{job}/delete`.

## Implementation Details

### 1. JobStore in `internal/tools/jobs.go` & `jobsview.go`
- Add `DeleteJob(id string) error` on `*JobStore`:
  - Acquires lock, finds job by `id`.
  - If running, terminates the job (similar to `KillJob(id)`).
  - Deletes `id` from `s.jobs`.
  - Calls `s.save()` to rewrite `.moony/jobs.json` without the deleted job.
- Expose `DeleteJob(id string) error` on `*Registry`.

### 2. HTTP Routes in `internal/web/jobs.go` & `server.go`
- Add `handleJobDelete(w http.ResponseWriter, r *http.Request)`:
  - Extracts session and job ID.
  - Calls `reg.DeleteJob(jobID)`.
  - Emits transient `jobs` event via `s.publishTransient(...)` for instant client updates.
  - Returns `200 OK` with `{"ok": true}`.
- In `server.go`, register:
  - `DELETE /api/sessions/{id}/jobs/{job}` -> `handleJobDelete`
  - `POST /api/sessions/{id}/jobs/{job}/delete` -> `handleJobDelete`

### 3. Frontend `jobstrip-line` in `internal/web/static/app-core.js`
- In `stripRow(j, folded)`:
  - Add a delete button using `stripIconBtn("trash", "Delete this job", ...)` in `actions`.
  - When clicked:
    - If `stripExpanded === j.id`, set `stripExpanded = null` and remove from `stripTails`.
    - Send `DELETE` to `/api/sessions/${current}/jobs/${encodeURIComponent(j.id)}`.
    - Optimistically filter out `j.id` from `stripJobs` and call `renderJobsStrip()`.

### 4. Manage Modal in `internal/web/static/app-manage.js`
- In `jobRow(j, list)`:
  - Add a "Delete" button in `actions`.
  - When clicked: send `DELETE` request, remove from local list, and refresh jobs view.

### 5. Verification Plan
- Unit tests:
  - `TestJobStoreDeleteJob` in `internal/tools/jobs_test.go` (tests deleting completed and running jobs, ensuring removal from list and persistence).
  - `TestJobDeleteEndpoint` in `internal/web/jobs_test.go` (tests `DELETE` route and 404 on unknown job).
- Browser E2E test in `scripts/verify_job_delete.mjs`:
  - Verifies trash icon renders in `jobstrip-line`.
  - Clicking trash deletes the job and updates the strip DOM.
  - Keyboard activation works.
  - Rebuilding `relay.exe` and `moony.exe`.
