package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Invoker runs a tool call on behalf of a tool that is itself running, through
// everything an ordinary call passes: permission, hooks, evidence and mutation
// observation. Only the agent that owns the turn offers one.
type Invoker interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// ErrNestedCallFailed is a nested call that ran and failed, or was refused; the
// output that accompanies it is what the model would have read.
var ErrNestedCallFailed = errors.New("tool call failed")

type invokerKey struct{}
type nestedKey struct{}

// WithInvoker binds the turn's invoker to a tool call.
func WithInvoker(ctx context.Context, inv Invoker) context.Context {
	if inv == nil {
		return ctx
	}
	return context.WithValue(ctx, invokerKey{}, inv)
}

// InvokerFrom returns the invoker bound to this tool call.
func InvokerFrom(ctx context.Context) (Invoker, bool) {
	inv, ok := ctx.Value(invokerKey{}).(Invoker)
	return inv, ok && inv != nil
}

// MarkNested marks a call as made by another tool rather than by the model.
func MarkNested(ctx context.Context) context.Context {
	return context.WithValue(ctx, nestedKey{}, true)
}

// IsNested reports whether this call was made by another tool.
func IsNested(ctx context.Context) bool {
	nested, _ := ctx.Value(nestedKey{}).(bool)
	return nested
}

// WithoutNested clears the nested marker on a derived context. A sub-agent
// spawned from inside a program runs its own agent loop: its model-issued calls
// are not program-nested, so the degradation nested tools opt into must stop at
// the child's entry.
func WithoutNested(ctx context.Context) context.Context {
	if !IsNested(ctx) {
		return ctx
	}
	return context.WithValue(ctx, nestedKey{}, false)
}

// NestedCallID is the id a nested call runs under: its caller's id, a dot, and
// its 1-based sequence within that caller. A recorded nested call carries no
// other mark of its caller, so readers of a recording recognise it by this id.
func NestedCallID(parentID string, seq int) string {
	return parentID + "." + strconv.Itoa(seq)
}

// IsNestedCallOf reports whether id is a NestedCallID made under parentID.
func IsNestedCallOf(id, parentID string) bool {
	if parentID == "" {
		return false
	}
	seq, ok := strings.CutPrefix(id, parentID+".")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(seq)
	return err == nil && n > 0 && strconv.Itoa(n) == seq
}
