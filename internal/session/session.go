// Package session persists agent conversations to disk so they can be resumed.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// Session is a persisted conversation.
type Session struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Model      string        `json:"model"`
	WorkingDir string        `json:"working_dir"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Messages   []llm.Message `json:"messages"`
	// Goal is an optional stop-condition for the session: the agent won't end a
	// turn until a judge model confirms the goal is met. Set via /goal.
	Goal string `json:"goal,omitempty"`
	// GoalCriteria are the concrete, verifiable completion criteria the goal is
	// judged against (the agent proposes them; the user can amend them). Empty
	// until generated/set.
	GoalCriteria []string `json:"goal_criteria,omitempty"`
	// GoalBudgetTokens is an optional token budget for the goal; when >0 the
	// agent stops once that many tokens have been spent pursuing it.
	GoalBudgetTokens int `json:"goal_budget_tokens,omitempty"`
	// GoalTokensUsed is the running token total spent pursuing the goal.
	GoalTokensUsed int `json:"goal_tokens_used,omitempty"`
	// GoalBlocked records that the goal was repeatedly judged unmet with no
	// progress and was marked blocked (stops nudging until the goal is amended).
	GoalBlocked bool `json:"goal_blocked,omitempty"`
	// GoalNotMetStreak counts consecutive turns that exhausted their nudges
	// without the judge confirming the goal — the basis for the blocked state.
	GoalNotMetStreak int `json:"goal_not_met_streak,omitempty"`
	// Checkpoint is a model-distilled snapshot of the session's state (goals,
	// key decisions, files touched, progress, open threads). It is refreshed
	// automatically as the session grows and re-injected as context so the
	// agent retains its understanding across compaction and resume. See the
	// agent's checkpoint.go.
	Checkpoint string `json:"checkpoint,omitempty"`
	// CheckpointMsgs records how many messages had accrued when Checkpoint was
	// last written, so the agent only re-distills after enough new activity.
	CheckpointMsgs int `json:"checkpoint_msgs,omitempty"`
	// Plan is an optional first-class plan artifact for the session: an ordered,
	// editable, approvable list of steps the agent drafts (in plan mode) and then
	// executes once approved. Distinct from the ephemeral todo list. See the
	// agent's plan.go.
	Plan *Plan `json:"plan,omitempty"`
	// TurnMetas records one entry per completed assistant turn (timestamp,
	// elapsed time, model, reasoning level, estimated cost), anchored to the
	// message count at turn end, so the web UI can replay each turn's footer
	// (datetime · duration · cost · model · think:level) when a saved session is
	// reopened.
	TurnMetas []TurnMeta `json:"turn_metas,omitempty"`
	// TaskMetas records one entry per completed `task` sub-agent delegation,
	// keyed by the task tool-call id (ParentID), so the web UI can replay each
	// task card's per-delegation footer (duration · cost · model · calls) when a
	// saved session is reopened — mirroring TurnMetas for the per-turn footer.
	TaskMetas []TaskMeta `json:"task_metas,omitempty"`
	// BoardMessages records the shared agent message-board posts (post_message)
	// across the session's turns, anchored by message count, so the web UI can
	// replay the multi-agent conversation — including sub-agent posts that never
	// enter the transcript — when a saved session is reopened.
	BoardMessages []BoardMessage `json:"board_messages,omitempty"`
	// InfoNotes records the small set of status/notice lines worth keeping in
	// history (currently: self-review's status updates), anchored by message
	// count, so they replay in place when a saved session is reopened. Most
	// agent.Info events stay live-only; only explicit noteInfo() calls land here.
	InfoNotes []InfoNote `json:"info_notes,omitempty"`
}

// TurnMeta is the persisted footer metadata for one completed assistant turn,
// rendered by the web UI as "datetime · duration · cost · model · think:level".
// MsgCount is len(Messages) when the turn finished, so replay can place the
// footer right after that turn's last message. CostUSD is the estimated dollar
// cost of the whole turn's token usage (all model calls). Calls is the number
// of model calls (loop steps) the turn made — the main cost driver, since each
// call resends the growing conversation. InTokens/OutTokens are the turn's total
// input/output tokens across all calls; CacheReadTokens is the cached-input
// portion (a subset of InTokens) for the prompt-cache hit-rate display.
type TurnMeta struct {
	Time            time.Time `json:"time"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	CostUSD         float64   `json:"cost_usd,omitempty"`
	Model           string    `json:"model,omitempty"`
	Reasoning       string    `json:"reasoning,omitempty"`
	MsgCount        int       `json:"msg_count"`
	Calls           int       `json:"calls,omitempty"`
	InTokens        int       `json:"in_tokens,omitempty"`
	OutTokens       int       `json:"out_tokens,omitempty"`
	CacheReadTokens int       `json:"cache_read_tokens,omitempty"`
	// SnapshotTurn is the file-undo snapshot turn id this turn was recorded
	// under, so /rewind reverts the right file snapshots even when the snapshot
	// counter has drifted ahead of the recorded-turn count (an errored or
	// cancelled turn bumps the counter but records no TurnMeta). 0 for turns
	// recorded before this field existed — their in-memory snapshots don't
	// survive a restart anyway, so a resumed session has nothing to revert.
	SnapshotTurn int `json:"snapshot_turn,omitempty"`
}

// TaskMeta is the persisted footer for one completed `task` sub-agent
// delegation, rendered under that task's card as "duration · cost · model ·
// calls". ParentID is the task tool-call id the footer belongs to, so replay
// can anchor it to the right card.
type TaskMeta struct {
	ParentID   string  `json:"parent_id"`
	Agent      string  `json:"agent,omitempty"`
	Model      string  `json:"model,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	Calls      int     `json:"calls,omitempty"`
	InTokens   int     `json:"in_tokens,omitempty"`
	OutTokens  int     `json:"out_tokens,omitempty"`
}

// BoardMessage is one persisted entry from the shared agent message board (the
// post_message / read_messages tools) so the web UI can replay the multi-agent
// conversation when a saved session reopens. A sub-agent's posts are otherwise
// lost on reopen — they run in the sub-agent's isolated context and never enter
// the parent transcript — so persisting them here (drained from the turn's
// board) is what makes the conversation survive, mirroring TurnMetas/TaskMetas.
// MsgCount is len(Messages) when the turn that produced it finished, so replay
// can emit the messages right after that turn. Seq is the message's 1-based id
// within its turn; To is "" for a broadcast.
type BoardMessage struct {
	Seq      int       `json:"seq"`
	From     string    `json:"from"`
	To       string    `json:"to,omitempty"`
	Text     string    `json:"text"`
	Time     time.Time `json:"time"`
	MsgCount int       `json:"msg_count"`
}

// InfoNote is a persisted status/notice line — e.g. a self-review status
// update ("Self-reviewing the diff…", "Self-review: no issues found. (model ·
// tokens · cost)") — anchored to the message count it followed, so it replays
// in the right place in the transcript when the session is reopened. Info
// events are normally live-only (pushed over the wire but not persisted);
// this is the opt-in exception for notices worth keeping in history.
type InfoNote struct {
	Text     string    `json:"text"`
	Time     time.Time `json:"time"`
	MsgCount int       `json:"msg_count"`
}

// PlanStatus is the lifecycle state of a session plan.
type PlanStatus string

const (
	// PlanDraft is a plan still being shaped — not yet approved for execution.
	PlanDraft PlanStatus = "draft"
	// PlanApproved is a plan the user has accepted; the agent executes it.
	PlanApproved PlanStatus = "approved"
	// PlanDone is an approved plan whose steps have all been carried out; the
	// agent stops injecting "carry it out" context for it. Reached automatically
	// when every mirrored todo is completed, or manually via /plan clear.
	PlanDone PlanStatus = "done"
)

// Plan is a first-class, persisted plan artifact: a titled, ordered list of
// steps the agent proposes and the user can amend before approving.
type Plan struct {
	Title  string     `json:"title,omitempty"`
	Steps  []PlanStep `json:"steps"`
	Status PlanStatus `json:"status"`
}

// PlanStep is a single ordered step of a plan.
type PlanStep struct {
	// Title is a short imperative description of the step.
	Title string `json:"title"`
	// Detail is optional clarifying context for the step.
	Detail string `json:"detail,omitempty"`
}

// New creates an empty session with a fresh id.
func New(model, workingDir string) *Session {
	now := time.Now()
	return &Session{
		ID:         newID(now),
		Model:      model,
		WorkingDir: workingDir,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// newID returns a sortable, unique session id like 20060102-150405-ab12.
func newID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Store reads and writes sessions under a directory.
type Store struct {
	dir string
	// crypto is the resolved encryption state (never nil after NewStoreAt). When
	// disabled (MN_SESSION_ENCRYPT=off) Save writes plaintext; Load always reads
	// legacy plaintext transparently regardless of mode.
	crypto *cryptoConfig
}

// NewStore opens (creating if needed) the session store under the user config
// directory: ~/.config/moony/sessions.
func NewStore() (*Store, error) {
	dir, err := config.GlobalConfigPath("sessions")
	if err != nil {
		return nil, err
	}
	return NewStoreAt(dir)
}

// NewStoreAt opens (creating if needed) a session store rooted at dir.
func NewStoreAt(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cc, err := resolveCrypto(dir)
	if err != nil {
		return nil, err
	}
	if err := cc.verifyKey(dir); err != nil {
		return nil, err
	}
	return &Store{dir: dir, crypto: cc}, nil
}

// Dir returns the directory backing the store.
func (s *Store) Dir() string { return s.dir }

// Save writes the session to disk, refreshing its title and timestamp.
func (s *Store) Save(sess *Session) error {
	sess.UpdatedAt = time.Now()
	if sess.Title == "" {
		sess.Title = deriveTitle(sess.Messages)
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if s.crypto != nil && s.crypto.enabled {
		data, err = s.crypto.encrypt(data)
		if err != nil {
			return fmt.Errorf("encrypt session %s: %w", sess.ID, err)
		}
		perm = 0o600 // ciphertext, but no reason to leave it world-readable
	}
	tmp := filepath.Join(s.dir, sess.ID+".json.tmp")
	final := filepath.Join(s.dir, sess.ID+".json")
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Load reads a session by id.
func (s *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	// Encrypted blobs carry our magic header; anything else is legacy plaintext
	// JSON and is read as-is (transparent migration: it re-saves encrypted).
	if isEncrypted(data) {
		if s.crypto == nil || !s.crypto.enabled {
			return nil, fmt.Errorf("session %s is encrypted but encryption is disabled (unset MN_SESSION_ENCRYPT=off / provide the key)", id)
		}
		data, err = s.crypto.decrypt(data)
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("corrupt session %s: %v", id, err)
	}
	return &sess, nil
}

// Delete removes a session from disk by id. A missing session is not an error,
// so deletes are idempotent. The id must be a bare session id (no path
// separators) so deletion stays confined to the store directory.
func (s *Store) Delete(id string) error {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return fmt.Errorf("invalid session id %q", id)
	}
	_ = os.Remove(filepath.Join(s.dir, id+".json.tmp")) // best-effort stray temp
	if err := os.Remove(filepath.Join(s.dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns all sessions, most recently updated first.
func (s *Store) List() ([]*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if sess, err := s.Load(id); err == nil {
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ListByDir returns sessions for a working directory, most-recently-updated
// first.
func (s *Store) ListByDir(workingDir string) ([]*Session, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(all))
	for _, sess := range all {
		if sess.WorkingDir == workingDir {
			out = append(out, sess)
		}
	}
	return out, nil
}

// Latest returns the most recently updated session for a working directory.
func (s *Store) Latest(workingDir string) (*Session, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, sess := range all {
		if sess.WorkingDir == workingDir {
			return sess, nil
		}
	}
	return nil, nil
}

// deriveTitle builds a short title from the first user text message.
func deriveTitle(messages []llm.Message) string {
	for _, m := range messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, c := range m.Content {
			if c.Type == llm.BlockText && strings.TrimSpace(c.Text) != "" {
				t := strings.TrimSpace(strings.ReplaceAll(c.Text, "\n", " "))
				if len(t) > 60 {
					t = t[:60] + "…"
				}
				return t
			}
		}
	}
	return "untitled session"
}

// Rewrap re-encrypts every session under the store's currently-resolved key.
// Use it to rotate keys or to encrypt a directory of legacy plaintext sessions:
// point the store's key at the NEW key (via env) and call Rewrap — each session
// is loaded (decrypting with whatever key/plaintext it currently has, only
// possible for files already under the current key or plaintext) and re-saved
// under the current key. Returns the number of sessions rewrapped.
//
// For a true key ROTATION (old key -> new key) run it in two phases the caller
// controls: it is simplest to load with the old key and save with the new; this
// method handles the common cases of (a) encrypting existing plaintext and
// (b) re-saving files already readable under the current key.
func (s *Store) Rewrap() (int, error) {
	if s.crypto == nil || !s.crypto.enabled {
		return 0, fmt.Errorf("cannot rewrap: session encryption is disabled")
	}
	sessions, err := s.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sess := range sessions {
		if err := s.Save(sess); err != nil {
			return n, fmt.Errorf("rewrap %s: %w", sess.ID, err)
		}
		n++
	}
	return n, nil
}

// Encrypted reports whether the store writes encrypted sessions.
func (s *Store) Encrypted() bool { return s.crypto != nil && s.crypto.enabled }
