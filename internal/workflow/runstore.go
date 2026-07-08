package workflow

// Run persistence: every Preview run is recorded to disk so the Agent Builder has
// run history and so a paused/failed run can be resumed later (durable runs). A
// RunRecord captures the input, status, the variable bus + completed-node frontier
// (for resume), a compact per-node trace (for history display), and accounting.
// Records live at <config>/workflow-runs/<runId>.json, pruned to a recent cap.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// Run status values.
const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunPaused    = "paused" // stopped at a user-approval node; resumable
)

// TraceEntry is one compact, persisted step of a run (no streamed deltas).
type TraceEntry struct {
	NodeID     string    `json:"nodeId"`
	NodeType   string    `json:"nodeType,omitempty"`
	Label      string    `json:"label,omitempty"`
	Status     string    `json:"status"` // completed | failed | paused | retry
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	Usage      NodeUsage `json:"usage,omitempty"`
	Model      string    `json:"model,omitempty"`
	DurationMS int64     `json:"durationMs,omitempty"`
}

// RunRecord is the persisted state of one run.
type RunRecord struct {
	ID           string         `json:"id"`
	WorkflowID   string         `json:"workflowId"`
	WorkflowName string         `json:"workflowName,omitempty"`
	Input        string         `json:"input"`
	Status       string         `json:"status"`
	Vars         map[string]any `json:"vars,omitempty"`        // variable bus (for resume)
	CurrentNode  string         `json:"currentNode,omitempty"` // the node to (re)start at on resume
	Trace        []TraceEntry   `json:"trace,omitempty"`
	Output       string         `json:"output,omitempty"` // final output (on completion)
	Error        string         `json:"error,omitempty"`
	CostUSD      float64        `json:"costUSD,omitempty"`
	StartedAt    string         `json:"startedAt,omitempty"`
	UpdatedAt    string         `json:"updatedAt,omitempty"`
}

// maxRunsKept bounds how many run records are retained per store (newest first);
// older ones are pruned on save so the dir doesn't grow unbounded.
const maxRunsKept = 200

// RunStore persists RunRecords as one JSON file each.
type RunStore struct{ dir string }

// OpenRuns returns a RunStore rooted at <GlobalConfigDir>/workflow-runs.
func OpenRuns() (*RunStore, error) {
	base, err := config.GlobalConfigDir()
	if err != nil {
		return nil, err
	}
	return &RunStore{dir: filepath.Join(base, "workflow-runs")}, nil
}

// OpenRunsAt returns a RunStore rooted at an explicit dir (tests).
func OpenRunsAt(dir string) *RunStore { return &RunStore{dir: dir} }

func (s *RunStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

// Save writes a record (atomically), stamps UpdatedAt, and prunes old records.
func (s *RunStore) Save(rec *RunRecord) error {
	if rec == nil || !validID(rec.ID) {
		return os.ErrInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if rec.StartedAt == "" {
		rec.StartedAt = now
	}
	rec.UpdatedAt = now
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+rec.ID+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path(rec.ID)); err != nil {
		return err
	}
	s.prune()
	return nil
}

// Load reads one run record by id.
func (s *RunStore) Load(id string) (*RunRecord, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var rec RunRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// List returns run records for a workflow (all when workflowID==""), newest first.
func (s *RunStore) List(workflowID string) ([]RunRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RunRecord{}, nil
		}
		return nil, err
	}
	out := make([]RunRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		if workflowID != "" && rec.WorkflowID != workflowID {
			continue
		}
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

// prune deletes the oldest records beyond maxRunsKept.
func (s *RunStore) prune() {
	all, err := s.List("")
	if err != nil || len(all) <= maxRunsKept {
		return
	}
	for _, rec := range all[maxRunsKept:] {
		os.Remove(s.path(rec.ID))
	}
}
