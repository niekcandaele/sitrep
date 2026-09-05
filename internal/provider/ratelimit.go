package provider

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// RequestPolicy is the TUI admission decision carried by one outbound Tracker
// request. Epoch advances when a refusal or exhausted budget invalidates earlier
// work; ProbeToken identifies the one explicit request allowed through an
// unknown-reset hold. Providers do not schedule from this value. Jira uses Epoch
// only to keep its internal catalogue barrier from admitting work launched under
// stale policy.
type RequestPolicy struct {
	Epoch      uint64
	ProbeToken uint64
}

type requestPolicyContextKey struct{}

// WithRequestPolicy attaches the caller's Tracker admission decision to ctx.
func WithRequestPolicy(ctx context.Context, policy RequestPolicy) context.Context {
	return context.WithValue(ctx, requestPolicyContextKey{}, policy)
}

// RequestPolicyFromContext returns the admission decision attached by the TUI.
// A one-shot caller that attaches nothing remains in the zero epoch.
func RequestPolicyFromContext(ctx context.Context) RequestPolicy {
	policy, _ := ctx.Value(requestPolicyContextKey{}).(RequestPolicy)
	return policy
}

type rateLimitCollectorKey struct{}

// WithRateLimitCollector attaches the budget collector a driver reports into.
// Drivers differ in wire format, so each parses its own headers or fields, but
// every one of them converges on this single context key.
func WithRateLimitCollector(ctx context.Context, collector *RateLimitBudgetCollector) context.Context {
	return context.WithValue(ctx, rateLimitCollectorKey{}, collector)
}

// RateLimitCollectorFromContext returns the collector attached by the caller, or
// nil when nothing is collecting.
func RateLimitCollectorFromContext(ctx context.Context) *RateLimitBudgetCollector {
	collector, _ := ctx.Value(rateLimitCollectorKey{}).(*RateLimitBudgetCollector)
	return collector
}

// RateLimitRefusal is the conservative policy meaning of the bounded portion of
// one Provider error graph. Err is a representative classified refusal that
// remains inspectable. KnownReset is true only when ResetAt is strictly after the
// caller's now. ExpiredOnly is true only when every refusal observed inside the
// traversal bound carries an explicit absolute reset that is not after now.
// Provider stopping remains conservative; TUI admission may treat such an
// already-elapsed deadline differently from absent timing.
type RateLimitRefusal struct {
	Err         error
	ResetAt     time.Time
	KnownReset  bool
	ExpiredOnly bool
}

// InspectRateLimitRefusal walks at most maxRateLimitErrorNodes nodes from both
// single- and multi-error unwrap graphs. Cancellation leaves are control flow,
// not refusals. Within that safety window, a refusal with no usable timing fact
// dominates the result. Otherwise the latest future reset wins over elapsed
// absolute resets; when every observed refusal is elapsed, ExpiredOnly reports
// it without creating a TUI hold. Multi-error traversal order is owned by each
// wrapper. A bounded DetailFailures sorts Ticket IDs; an oversized aggregate
// exposes only its deterministic bounds sentinel, while native plural transports
// retain already-inspected refusal provenance as a shallow sibling.
func InspectRateLimitRefusal(err error, now time.Time) (RateLimitRefusal, bool) {
	var unknown *Error
	var expired *Error
	var latest RateLimitRefusal
	var latestErr *Error

	walkErrorGraph(err, func(classified *Error) {
		deadline, known := classified.RateLimit.Deadline(now)
		if !known {
			if unknown == nil || stableRateErrorLess(classified, unknown) {
				unknown = classified
			}
			return
		}
		deadline = deadline.UTC()
		if !deadline.After(now) {
			if expired == nil || stableRateErrorLess(classified, expired) {
				expired = classified
			}
			return
		}
		if latest.Err == nil || deadline.After(latest.ResetAt) ||
			(deadline.Equal(latest.ResetAt) && stableRateErrorLess(classified, latestErr)) {
			latestErr = classified
			latest = RateLimitRefusal{
				Err:        classified,
				ResetAt:    deadline,
				KnownReset: true,
			}
		}
	})

	if unknown != nil {
		return RateLimitRefusal{Err: unknown}, true
	}
	if latest.Err != nil {
		return latest, true
	}
	if expired != nil {
		return RateLimitRefusal{Err: expired, ExpiredOnly: true}, true
	}
	return RateLimitRefusal{}, false
}

func stableRateErrorLess(left, right *Error) bool {
	leftReset := left.RateLimit.ResetAt.UTC()
	rightReset := right.RateLimit.ResetAt.UTC()
	if leftReset.IsZero() != rightReset.IsZero() {
		return !leftReset.IsZero()
	}
	if !leftReset.Equal(rightReset) {
		return leftReset.Before(rightReset)
	}
	if left.RateLimit.RetryAfter != right.RateLimit.RetryAfter {
		return left.RateLimit.RetryAfter < right.RateLimit.RetryAfter
	}
	leftType := reflect.TypeOf(left.Err)
	rightType := reflect.TypeOf(right.Err)
	if leftType != rightType {
		return stableTypeName(leftType) < stableTypeName(rightType)
	}
	return reflect.ValueOf(left).Pointer() < reflect.ValueOf(right).Pointer()
}

func stableTypeName(value reflect.Type) string {
	if value == nil {
		return ""
	}
	return value.PkgPath() + "/" + value.String()
}

type errorGraphKey struct {
	value  error
	typeOf reflect.Type
}

const maxRateLimitErrorNodes = 4096

func walkErrorGraph(root error, visit func(*Error)) {
	queue := []error{root}
	seen := make(map[errorGraphKey]struct{})
	for index := 0; index < len(queue) && index < maxRateLimitErrorNodes; index++ {
		err := queue[index]
		// Exact cancellation leaves are skipped; wrappers still need graph traversal
		// because a wrapper can carry a refusal beside a cancellation child.
		if err == nil || termtext.IsTypedNilError(err) || err == context.Canceled || err == context.DeadlineExceeded { //nolint:errorlint
			continue
		}
		if errorGraphNodeSeen(err, seen) {
			continue
		}
		// This traversal owns typed-nil and cycle safety, so standard errors.As must
		// not recursively inspect the untrusted graph here.
		classified, ok := err.(*Error) //nolint:errorlint
		if ok && classified != nil && classified.Kind == KindRateLimit {
			visit(classified)
		}

		// Direct unwrap assertions are the cycle-safe traversal mechanism itself.
		switch wrapped := err.(type) { //nolint:errorlint
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			remaining := maxRateLimitErrorNodes - len(queue)
			if len(children) > remaining {
				children = children[:remaining]
			}
			queue = append(queue, children...)
		case interface{ Unwrap() error }:
			if len(queue) < maxRateLimitErrorNodes {
				queue = append(queue, wrapped.Unwrap())
			}
		}
	}
}

func errorGraphNodeSeen(err error, seen map[errorGraphKey]struct{}) bool {
	value := reflect.ValueOf(err)
	if !value.Comparable() {
		// Pointer identity is not value identity for dynamically non-comparable
		// values. The traversal budget—not an unsound key—bounds their cycles.
		return false
	}
	key := errorGraphKey{value: err, typeOf: value.Type()}
	if _, visited := seen[key]; visited {
		return true
	}
	seen[key] = struct{}{}
	return false
}

// RateLimitBudgetCollector conservatively retains the tightest valid budget
// observed while one Resolve is in flight.
type RateLimitBudgetCollector struct {
	mu     sync.Mutex
	budget model.RateLimitBudget
}

// Observe records a valid budget when it is more conservative than the current
// value. Equal remaining capacity uses the later reset as the deterministic
// conservative tie-break.
func (c *RateLimitBudgetCollector) Observe(budget model.RateLimitBudget) {
	if !budget.Valid() {
		return
	}
	budget.ResetsAt = budget.ResetsAt.UTC()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.budget.Valid() || budget.Remaining < c.budget.Remaining ||
		(budget.Remaining == c.budget.Remaining && budget.ResetsAt.After(c.budget.ResetsAt)) {
		c.budget = budget
	}
}

// Budget returns the collected budget by value.
func (c *RateLimitBudgetCollector) Budget() model.RateLimitBudget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.budget
}
