package workflow

// Structural validation for parallel/join regions. The execution engine
// (runRegion / walkBranch) assumes a *well-nested* graph: every parallel node
// fans out to ≥2 branches that all reconverge on a single shared join, and each
// join continues via at most one edge. Validate checks those invariants up front
// so the builder can flag a malformed region with a clear message instead of the
// run failing partway through.
//
// Validate is a no-op (returns nil) for any graph WITHOUT a parallel node, so it
// never affects existing linear / if-else / while workflows. It's region-aware:
// a nested parallel inside a branch is resolved to its own join and stepped over,
// so proper nesting validates correctly.

import (
	"fmt"
)

// Validate reports the first structural problem with the workflow's parallel
// regions, or nil if they're well-formed (or absent). It's intended to run just
// before execution.
func Validate(wf *Workflow) error {
	byID := make(map[string]*Node, len(wf.Nodes))
	hasRegion := false
	for i := range wf.Nodes {
		byID[wf.Nodes[i].ID] = &wf.Nodes[i]
		if wf.Nodes[i].Type == NodeParallel || wf.Nodes[i].Type == NodeJoin {
			hasRegion = true
		}
	}
	if !hasRegion {
		return nil // nothing to check — pure linear/branch/loop graph
	}

	out := make(map[string][]Edge)
	for _, e := range wf.Edges {
		out[e.Source] = append(out[e.Source], e)
	}

	// Each join continues via at most one (non-note) edge, so post-join routing is
	// deterministic.
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		if n.Type != NodeJoin {
			continue
		}
		if cnt := nonNoteTargets(out[n.ID], byID); cnt > 1 {
			return fmt.Errorf("join %q has %d outgoing edges; a join may continue via at most one", nodeLabel(n), cnt)
		}
	}

	// Every parallel region must be well-formed: ≥2 branches reconverging on one
	// shared join. matchJoin reports the shared join (and errors otherwise).
	matchedJoins := map[string]string{} // joinID -> the parallel id that owns it
	for i := range wf.Nodes {
		p := &wf.Nodes[i]
		if p.Type != NodeParallel {
			continue
		}
		joinID, err := matchJoin(p, out, byID)
		if err != nil {
			return err
		}
		if owner, dup := matchedJoins[joinID]; dup {
			return fmt.Errorf("join %q is the convergence of more than one parallel (%q and %q); each parallel needs its own join", nodeLabel(byID[joinID]), owner, p.ID)
		}
		matchedJoins[joinID] = p.ID
	}

	// Every join must belong to some parallel (no orphan joins).
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		if n.Type == NodeJoin {
			if _, ok := matchedJoins[n.ID]; !ok {
				return fmt.Errorf("join %q is not the convergence of any parallel node", nodeLabel(n))
			}
		}
	}
	return nil
}

// matchJoin returns the single join node id that all of p's branches reconverge
// on, or an error if p has fewer than 2 branches, a branch never reaches a join,
// or branches reach different joins.
func matchJoin(p *Node, out map[string][]Edge, byID map[string]*Node) (string, error) {
	var entries []*Node
	for _, e := range out[p.ID] {
		tgt := byID[e.Target]
		if tgt == nil || tgt.Type == NodeNote {
			continue
		}
		entries = append(entries, tgt)
	}
	if len(entries) < 2 {
		return "", fmt.Errorf("parallel %q needs at least 2 branches, has %d", nodeLabel(p), len(entries))
	}
	joinID := ""
	for _, entry := range entries {
		j, err := branchJoin(entry, p, out, byID)
		if err != nil {
			return "", err
		}
		if j == "" {
			return "", fmt.Errorf("a branch of parallel %q does not reconverge at a join node", nodeLabel(p))
		}
		if joinID == "" {
			joinID = j
		} else if j != joinID {
			return "", fmt.Errorf("branches of parallel %q reconverge on different joins (%q vs %q)", nodeLabel(p), joinID, j)
		}
	}
	return joinID, nil
}

// branchJoin walks forward from a branch entry and returns the id of the first
// join the branch converges on (the same node walkBranch stops at), or "" if a
// path escapes to an end/dead-end without hitting a join. A nested parallel is
// resolved to its own join and stepped over. Cycles (while loops) are bounded by
// a visited set. Returns an error if the branch can reach two different joins
// (e.g. an if-else inside the branch whose arms converge on different joins).
func branchJoin(entry, owner *Node, out map[string][]Edge, byID map[string]*Node) (string, error) {
	visited := map[string]bool{}
	var found string
	// Deterministic DFS so error messages are stable.
	var walk func(n *Node) error
	walk = func(n *Node) error {
		if n == nil || visited[n.ID] {
			return nil
		}
		visited[n.ID] = true
		switch n.Type {
		case NodeJoin:
			if found == "" {
				found = n.ID
			} else if found != n.ID {
				return fmt.Errorf("a branch of parallel %q can reach two different joins (%q and %q)", nodeLabel(owner), found, n.ID)
			}
			return nil // don't descend past a join
		case NodeEnd:
			return nil // a path that ends without a join — handled by the "" check upstream
		case NodeParallel:
			if n.ID != owner.ID {
				// Nested region: resolve its join, then continue past it.
				nj, err := matchJoin(n, out, byID)
				if err != nil {
					return err
				}
				return walkEdges(out[nj], walk, byID)
			}
		}
		return walkEdges(out[n.ID], walk, byID)
	}
	if err := walk(entry); err != nil {
		return "", err
	}
	return found, nil
}

// walkEdges visits each non-note edge target in deterministic (declaration) order.
func walkEdges(edges []Edge, walk func(*Node) error, byID map[string]*Node) error {
	for _, e := range edges {
		tgt := byID[e.Target]
		if tgt == nil || tgt.Type == NodeNote {
			continue
		}
		if err := walk(tgt); err != nil {
			return err
		}
	}
	return nil
}

// nonNoteTargets counts how many edges point at a non-note, existing node.
func nonNoteTargets(edges []Edge, byID map[string]*Node) int {
	n := 0
	for _, e := range edges {
		tgt := byID[e.Target]
		if tgt != nil && tgt.Type != NodeNote {
			n++
		}
	}
	return n
}
