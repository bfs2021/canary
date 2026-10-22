package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/osauer/canary/v2/internal/rpc"
)

// runLocks reads and edits the owner's keep list. A locked line is never
// selected by a bulk reduction or an automatic one; it places no order.
func runLocks(ctx context.Context, env *Env, args []string) int {
	fs := flagSet(env, "locks")
	conID := fs.Int("con-id", 0, "exact contract ID of the line")
	role := fs.String("role", rpc.HoldingLockRoleCore, "hedge or core")
	revision := fs.Int64("expected-revision", -1, "revision fence; otherwise read once")
	id := fs.String("request-id", "", "immutable request ID; otherwise generated")
	jsonOut := fs.Bool("json", false, "emit the accepted list or an explicit conflict as JSON")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	op := "list"
	if fs.NArg() > 0 {
		op = fs.Arg(0)
	}
	var out rpc.HoldingLocks
	method, params := rpc.MethodHoldingLocksList, any(nil)
	switch op {
	case "list":
		if fs.NArg() > 1 || *conID != 0 || *revision != -1 || *id != "" {
			return fail(env, "usage: canary locks list [--json]")
		}
	case "lock", "unlock":
		if fs.NArg() != 2 || *conID <= 0 {
			return fail(env, "usage: canary locks lock|unlock SYMBOL --con-id ID [--role hedge|core] [--expected-revision N] [--request-id ID] [--json]")
		}
		var current rpc.HoldingLocks
		if err := env.Conn.Call(ctx, rpc.MethodHoldingLocksList, nil, &current); err != nil {
			return fail(env, "locks: read current list: %v", err)
		}
		if *revision == -1 {
			*revision = current.Revision
		}
		next := slices.DeleteFunc(slices.Clone(current.Locks), func(l rpc.HoldingLock) bool { return l.ConID == *conID })
		if op == "lock" {
			next = append(next, rpc.HoldingLock{ConID: *conID, Symbol: fs.Arg(1), Role: *role})
		}
		next, err := rpc.NormalizeHoldingLocks(next)
		if err != nil {
			return fail(env, "locks: %v", err)
		}
		if *id == "" {
			*id = rand.Text()
		}
		if err = rpc.ValidateWatchlistMutation(*revision, *id); err != nil {
			return fail(env, "locks: %v", err)
		}
		method, params = rpc.MethodHoldingLocksReplace, rpc.HoldingLocksReplaceRequest{Locks: next, ExpectedRevision: *revision, RequestID: *id}
	default:
		return fail(env, "usage: canary locks list|lock|unlock [--json]")
	}
	if err := env.Conn.Call(ctx, method, params, &out); err != nil {
		var rpcErr *rpc.Error
		if *jsonOut && errors.As(err, &rpcErr) && rpcErr.Code == rpc.CodeHoldingLocksConflict {
			_ = printJSON(env, struct {
				Error *rpc.Error `json:"error"`
			}{rpcErr})
		}
		if op != "list" {
			return fail(env, "locks: %v; retry unchanged terms with expected_revision=%d request_id=%s", err, *revision, *id)
		}
		return fail(env, "locks: %v", err)
	}
	if *jsonOut {
		return printJSON(env, out)
	}
	renderHoldingLocks(env, out)
	return 0
}

func renderHoldingLocks(env *Env, out rpc.HoldingLocks) {
	if len(out.Locks) == 0 {
		fmt.Fprintln(env.Stdout, "Locked holdings · none · every line can be reduced in bulk")
		return
	}
	fmt.Fprintf(env.Stdout, "Locked holdings · %d · never selected by a bulk or automatic reduction\n", len(out.Locks))
	for _, l := range out.Locks {
		fmt.Fprintf(env.Stdout, "%s\t%s\tcontract %d\n", l.Symbol, strings.ToLower(l.Role), l.ConID)
	}
}
