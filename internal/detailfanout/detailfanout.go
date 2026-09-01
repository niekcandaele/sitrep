// Package detailfanout is the shared policy for reading a whole Watchlist's
// Details at once.
//
// ADR-0003's split by view still holds: a list refresh never calls FetchDetail,
// and no Detail field migrates onto the thin Ticket. What this package permits
// is narrower — a caller may read Details for every member of a Watchlist only
// in response to an explicit user action, never from a refresh, poll or render.
package detailfanout

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// Fetch reads Details for a complete planned slice. Its result map is folded by
// Run in input order, so a Provider's map iteration can never affect progress or
// rendered output.
type Fetch func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error)

// FromProvider adapts a Provider to Fetch with its declared Capabilities.
func FromProvider(p provider.Provider) Fetch {
	return func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		details, err := p.FetchDetails(ctx, ids)
		return details, p.Capabilities(), NormalizeError(err)
	}
}

// Plan returns the Ticket IDs a caller still has to fetch, in canonical order:
// the Watchlist's own order, skipping empty IDs, duplicates, and anything the
// have predicate already reports holding. A nil have means the caller holds
// nothing.
func Plan(tickets []model.Ticket, have func(model.TicketID) bool) []model.TicketID {
	var ids []model.TicketID
	planned := make(map[model.TicketID]bool, len(tickets))
	for _, t := range tickets {
		if t.ID == "" || planned[t.ID] {
			continue
		}
		if have != nil && have(t.ID) {
			continue
		}
		planned[t.ID] = true
		ids = append(ids, t.ID)
	}
	return ids
}

// Outcome is one requested Ticket's successful Detail or per-Ticket failure.
type Outcome struct {
	ID     model.TicketID
	Detail model.Detail
	Caps   model.Capabilities
	Err    error
}

// Parallelism is the Frontier scheduler's bound on active plural generation
// commands, including retiring cancellation-ignoring work. Run itself issues one
// logical plural Provider call and does not consume this constant.
const Parallelism = 4

// UnreadableLinksNotice describes how unreadable member Links constrain the
// blocking graph. An empty string means every planned read succeeded.
func UnreadableLinksNotice(failed int) string {
	switch failed {
	case 0:
		return ""
	case 1:
		return "1 Ticket's Links could not be read; anything it blocks is not Actionable"
	default:
		return fmt.Sprintf("%d Tickets' Links could not be read; anything they block is not Actionable", failed)
	}
}

type detailFailuresCarrier interface {
	error
	DetailFailuresByTicket() map[model.TicketID]error
}

type normalizedError struct {
	message        string
	original       error
	children       []error
	classification *provider.Error
	detailFailures *provider.DetailFailures

	// oversizedDetailFailures retains membership without exposing constituent
	// errors through errors.As. Run probes only its already-planned IDs, so an
	// oversized Provider map cannot widen normalization work.
	oversizedDetailFailures map[model.TicketID]error
	bounded                 bool
	nilCause                bool
}

func (e *normalizedError) Error() string   { return e.message }
func (e *normalizedError) Unwrap() []error { return e.children }

// Is keeps the replaced wrapper itself reachable without exposing its unsafe
// children or custom matchers to standard traversal.
func (e *normalizedError) Is(target error) bool {
	if sameErrorIdentity(e.original, target) {
		return true
	}
	// Custom Is/As methods are intentionally never delegated: their original
	// error graph was untrusted at this boundary.
	return false
}

// sameErrorIdentity avoids interface equality on custom non-comparable errors.
// Slice values have no sound identity: distinct slice values can have identical
// pointer, length, and capacity, so they intentionally are never matched.
func sameErrorIdentity(original error, target error) bool {
	left, right := reflect.ValueOf(original), reflect.ValueOf(target)
	if !left.IsValid() || !right.IsValid() || left.Type() != right.Type() {
		return false
	}
	if left.Comparable() && right.Comparable() {
		return left.Equal(right)
	}
	if left.Kind() == reflect.Map {
		return !left.IsNil() && left.Pointer() == right.Pointer() && left.Len() == right.Len()
	}
	return false
}

// As preserves safe wrapper classification. A normalized DetailFailures clone
// wins over the malformed original so callers never receive its unsafe map.
func (e *normalizedError) As(target any) bool {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return false
	}
	targetType := value.Elem().Type()
	if e.detailFailures != nil {
		candidate := reflect.ValueOf(e.detailFailures)
		if candidate.Type().AssignableTo(targetType) {
			value.Elem().Set(candidate)
			return true
		}
	}
	if e.classification != nil {
		candidate := reflect.ValueOf(e.classification)
		if candidate.Type().AssignableTo(targetType) {
			value.Elem().Set(candidate)
			return true
		}
	}
	// Do not invoke the replaced node's As method or expose its original concrete
	// value. Only explicit safe clones retain classifications after replacement.
	return false
}

// NormalizeError makes the plural Detail boundary safe to inspect. It walks the
// unwrap graph without errors.As, replacing typed-nil nodes before Error,
// Unwrap, errors.Is, or errors.As can invoke a nil receiver. An error whose
// graph needs no replacement remains unchanged, preserving its safe custom
// Is/As behavior; replaced wrappers expose only safe identities and types.
func NormalizeError(err error) error {
	normalized, _ := normalizeErrorGraph(err)
	return normalized
}

const (
	maxErrorUnwrapDepth    = 64
	maxErrorUnwrapNodes    = 256
	maxErrorUnwrapChildren = 64
)

type boundedUnwrapError struct{}

func (boundedUnwrapError) Error() string {
	return "provider: FetchDetails returned an error graph exceeding safe unwrap bounds"
}

type normalizeErrorState struct {
	nodes                      int
	preservedRateLimitFacts    map[provider.RateLimitMetadata]struct{}
	preservedCancellationFacts map[error]struct{}
}

type safeErrorGraphFacts struct {
	rateLimits    []*provider.Error
	cancellations []error
}

type rateLimitScanNode struct {
	err   error
	depth int
}

type rateLimitScanKey struct {
	value  error
	typeOf reflect.Type
}

// safeErrorGraphRepresentatives finds direct refusal classifications and exact
// cancellation leaves in bounded breadth-first order. Pointer cycles are
// de-duplicated before they consume the scan budget, so a shallow sibling cannot
// be starved by a cyclic branch. Returned refusal clones retain policy facts
// without carrying untrusted children. This scan uses the normalizer's depth,
// node, and per-node child window; the Provider policy inspector's larger
// raw-graph budget does not widen this safety boundary.
func safeErrorGraphRepresentatives(root error) safeErrorGraphFacts {
	queue := make([]rateLimitScanNode, 0, maxErrorUnwrapNodes)
	seen := make(map[rateLimitScanKey]struct{})
	enqueue := func(err error, depth int) {
		if err == nil || depth >= maxErrorUnwrapDepth || len(queue) >= maxErrorUnwrapNodes || termtext.IsTypedNilError(err) {
			return
		}
		value := reflect.ValueOf(err)
		if value.Comparable() {
			key := rateLimitScanKey{value: err, typeOf: value.Type()}
			if _, exists := seen[key]; exists {
				return
			}
			seen[key] = struct{}{}
		}
		queue = append(queue, rateLimitScanNode{err: err, depth: depth})
	}
	enqueue(root, 0)

	facts := safeErrorGraphFacts{}
	rateLimitFacts := make(map[provider.RateLimitMetadata]struct{})
	cancellationFacts := make(map[error]struct{})
	for index := 0; index < len(queue) && index < maxErrorUnwrapNodes; index++ {
		node := queue[index]
		if classified, ok := node.err.(*provider.Error); ok && classified != nil && classified.Kind == provider.KindRateLimit { //nolint:errorlint // Direct classification avoids custom traversal.
			if _, exists := rateLimitFacts[classified.RateLimit]; !exists {
				rateLimitFacts[classified.RateLimit] = struct{}{}
				facts.rateLimits = append(facts.rateLimits, &provider.Error{
					Kind:      provider.KindRateLimit,
					Err:       errors.New("provider: FetchDetails contained a rate-limit refusal whose cause was not retained"),
					RateLimit: classified.RateLimit,
				})
			}
		}
		if node.err == context.Canceled || node.err == context.DeadlineExceeded { //nolint:errorlint // Exact semantic leaves are safe identities.
			if _, exists := cancellationFacts[node.err]; !exists {
				cancellationFacts[node.err] = struct{}{}
				facts.cancellations = append(facts.cancellations, node.err)
			}
		}

		remaining := maxErrorUnwrapNodes - len(queue)
		if remaining == 0 {
			continue
		}
		switch wrapped := node.err.(type) { //nolint:errorlint // Direct unwrap assertions are the bounded traversal mechanism.
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			limit := min(len(children), maxErrorUnwrapChildren)
			for _, child := range children[:limit] {
				before := len(queue)
				enqueue(child, node.depth+1)
				if len(queue) > before {
					remaining--
					if remaining == 0 {
						break
					}
				}
			}
		case interface{ Unwrap() error }:
			enqueue(wrapped.Unwrap(), node.depth+1)
		}
	}
	return facts
}

func (s *normalizeErrorState) observeSemanticFacts(err error) {
	if err == context.Canceled || err == context.DeadlineExceeded { //nolint:errorlint // Exact semantic leaves are safe identities.
		s.preservedCancellationFacts[err] = struct{}{}
	}
	classified, ok := err.(*provider.Error) //nolint:errorlint // Direct classification avoids traversing the unsafe graph.
	if !ok || classified == nil || classified.Kind != provider.KindRateLimit {
		return
	}
	s.preservedRateLimitFacts[classified.RateLimit] = struct{}{}
}

func (s *normalizeErrorState) missingSemanticRepresentatives(facts safeErrorGraphFacts) []error {
	missing := make([]error, 0, len(facts.rateLimits)+len(facts.cancellations))
	for _, representative := range facts.rateLimits {
		if _, preserved := s.preservedRateLimitFacts[representative.RateLimit]; !preserved {
			missing = append(missing, representative)
		}
	}
	for _, cancellation := range facts.cancellations {
		if _, preserved := s.preservedCancellationFacts[cancellation]; !preserved {
			missing = append(missing, cancellation)
		}
	}
	return missing
}

func safeProviderClassification(err error) *provider.Error {
	classified, ok := err.(*provider.Error) //nolint:errorlint // Direct classification avoids traversing the unsafe child.
	if !ok || classified == nil {
		return nil
	}
	return &provider.Error{
		Kind:      classified.Kind,
		Err:       errors.New("provider: FetchDetails returned a classified error containing an unsafe cause"),
		RateLimit: classified.RateLimit,
	}
}

func safeNilCauseProviderClassification(classified *provider.Error) *provider.Error {
	return &provider.Error{
		Kind:      classified.Kind,
		Err:       errors.New("provider: FetchDetails returned a classified error with a nil cause"),
		RateLimit: classified.RateLimit,
	}
}

func normalizeErrorGraph(err error) (error, bool) {
	facts := safeErrorGraphRepresentatives(err)
	state := &normalizeErrorState{
		preservedRateLimitFacts:    make(map[provider.RateLimitMetadata]struct{}),
		preservedCancellationFacts: make(map[error]struct{}),
	}
	normalized, changed := normalizeErrorGraphAt(err, 0, state)
	if !changed || !errorGraphBounded(normalized) {
		return normalized, changed
	}
	missing := state.missingSemanticRepresentatives(facts)
	if len(missing) == 0 {
		return normalized, changed
	}
	children := make([]error, 1, len(missing)+1)
	children[0] = normalized
	children = append(children, missing...)
	return &normalizedError{
		message:  normalized.Error(),
		children: children,
		bounded:  true,
	}, true
}

func normalizeErrorGraphAt(err error, depth int, state *normalizeErrorState) (error, bool) {
	if depth >= maxErrorUnwrapDepth || state.nodes >= maxErrorUnwrapNodes {
		return boundedUnwrapError{}, true
	}
	// This recognizes only the normalizer-owned wrapper; errors.As would recurse
	// into the original graph before the boundary has established it is safe.
	if _, alreadyNormalized := err.(*normalizedError); alreadyNormalized { //nolint:errorlint
		return err, false
	}
	state.nodes++
	if err == nil {
		return nil, false
	}
	if termtext.IsTypedNilError(err) {
		if reflect.TypeOf(err) == reflect.TypeOf((*provider.DetailFailures)(nil)) {
			return errors.New("provider: FetchDetails returned a typed-nil DetailFailures error"), true
		}
		return errors.New("provider: FetchDetails returned a typed-nil error"), true
	}
	if classified, ok := err.(*provider.Error); ok && classified != nil && classified.Err == nil { //nolint:errorlint // A direct trusted classification must carry a safe cause.
		safe := safeNilCauseProviderClassification(classified)
		return &normalizedError{
			message:        safe.Error(),
			original:       err,
			children:       []error{safe},
			classification: safe,
			nilCause:       true,
		}, true
	}
	state.observeSemanticFacts(err)

	// This exact marker check is deliberate: errors.As would traverse the graph
	// before its typed-nil constituents have been replaced.
	failures, detailFailure := err.(detailFailuresCarrier) //nolint:errorlint
	if detailFailure {
		rawFailures := failures.DetailFailuresByTicket()
		if len(rawFailures) > maxErrorUnwrapChildren {
			return &normalizedError{
				message:                 boundedUnwrapError{}.Error(),
				original:                err,
				children:                []error{boundedUnwrapError{}},
				oversizedDetailFailures: rawFailures,
				bounded:                 true,
			}, true
		}
		normalized := make(map[model.TicketID]error, len(rawFailures))
		changed, bounded, nilCause := false, false, false
		for id, failure := range rawFailures {
			safe, replaced := normalizeErrorGraphAt(failure, depth+1, state)
			normalized[id] = safe
			changed = changed || replaced
			bounded = bounded || errorGraphBounded(safe)
			nilCause = nilCause || errorGraphHasNilCause(safe)
		}
		if !changed {
			return err, false
		}
		aggregate := &provider.DetailFailures{Failures: normalized}
		return &normalizedError{
			message: aggregate.Error(), original: err,
			children: []error{aggregate}, detailFailures: aggregate,
			bounded: bounded, nilCause: nilCause,
		}, true
	}

	if many, ok := err.(interface{ Unwrap() []error }); ok {
		rawChildren := many.Unwrap()
		limit := min(len(rawChildren), maxErrorUnwrapChildren)
		children := make([]error, 0, limit+2)
		changed := len(rawChildren) > limit
		for _, child := range rawChildren[:limit] {
			safe, replaced := normalizeErrorGraphAt(child, depth+1, state)
			if safe != nil {
				children = append(children, safe)
			}
			changed = changed || replaced
		}
		if len(rawChildren) > limit {
			children = append(children, boundedUnwrapError{})
		}
		if !changed {
			return err, false
		}
		return newNormalizedError(err, children), true
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		child, changed := normalizeErrorGraphAt(one.Unwrap(), depth+1, state)
		if !changed {
			return err, false
		}
		var children []error
		if child != nil {
			children = []error{child}
		}
		return newNormalizedError(err, children), true
	}
	return err, false
}

func errorGraphBounded(err error) bool {
	//nolint:errorlint // This checks only normalizer-owned sentinels; errors.As would traverse an untrusted graph.
	switch err := err.(type) {
	case boundedUnwrapError:
		return true
	case *normalizedError:
		return err.bounded
	default:
		return false
	}
}

func errorGraphHasNilCause(err error) bool {
	//nolint:errorlint // Only the normalizer-owned wrapper carries this diagnostic provenance.
	normalized, ok := err.(*normalizedError)
	return ok && normalized.nilCause
}

func newNormalizedError(original error, children []error) error {
	bounded := false
	nilCause := false
	for _, child := range children {
		bounded = bounded || errorGraphBounded(child)
		nilCause = nilCause || errorGraphHasNilCause(child)
	}
	message := "provider: FetchDetails returned an error containing a typed-nil cause"
	if nilCause {
		message = "provider: FetchDetails returned an error containing a nil cause"
	}
	if bounded {
		message = boundedUnwrapError{}.Error()
	}
	classification := safeProviderClassification(original)
	if classification != nil && classification.Kind == provider.KindRateLimit {
		children = append(children, classification)
	}
	return &normalizedError{
		message:        message,
		original:       original,
		children:       children,
		classification: classification,
		bounded:        bounded,
		nilCause:       nilCause,
	}
}

// Run returns before dispatch when the planned slice is empty or ctx.Err() is
// non-nil at the pre-dispatch check. Otherwise, it calls f once with the full
// slice and emits outcomes in canonical input order. DetailFailures supply
// per-Ticket failures; an ordinary response-wide error preserves completed
// Details and fails each incomplete ID. Cancellation is control flow: completed
// successes are emitted, incomplete IDs are left unknown, and no unreadable-Links
// failures are manufactured for them.
func Run(ctx context.Context, f Fetch, ids []model.TicketID, emit func(Outcome)) error {
	if len(ids) == 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	details, caps, err := f(ctx, ids)
	err = NormalizeError(err)
	if details == nil {
		details = map[model.TicketID]model.Detail{}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		failures, _ := detailFailures(err, ids)
		for _, id := range ids {
			if _, failed := failures[id]; failed {
				continue
			}
			if detail, ok := details[id]; ok && detail.TicketID == id {
				emit(Outcome{ID: id, Detail: detail, Caps: caps})
			}
		}
		return ctxErr
	}

	failures, ordinary := detailFailures(err, ids)
	invalid, batchErr := invalidResults(ids, details, failures)
	for _, id := range ids {
		if failure, ok := invalid[id]; ok {
			emit(Outcome{ID: id, Err: failure})
			continue
		}
		if failure, ok := failures[id]; ok {
			emit(Outcome{ID: id, Err: failure})
			continue
		}
		if detail, ok := details[id]; ok {
			emit(Outcome{ID: id, Detail: detail, Caps: caps})
			continue
		}
		if ordinary != nil {
			emit(Outcome{ID: id, Err: ordinary})
			continue
		}
		emit(Outcome{ID: id, Err: fmt.Errorf("provider: FetchDetails omitted detail for %q", id)})
	}
	return batchErr
}

func oversizedFailureMembership(err error, ids []model.TicketID) map[model.TicketID]error {
	var failures map[model.TicketID]error
	queue := []*normalizedError{}
	if normalized, ok := err.(*normalizedError); ok { //nolint:errorlint // Only normalizer-owned metadata is safe to inspect here.
		queue = append(queue, normalized)
	}
	seen := make(map[*normalizedError]struct{})
	for len(queue) != 0 && len(seen) < maxErrorUnwrapNodes {
		normalized := queue[0]
		queue = queue[1:]
		if _, visited := seen[normalized]; visited {
			continue
		}
		seen[normalized] = struct{}{}
		if raw := normalized.oversizedDetailFailures; raw != nil {
			for _, id := range ids {
				if _, failed := raw[id]; !failed {
					continue
				}
				if failures == nil {
					failures = make(map[model.TicketID]error)
				}
				failures[id] = boundedUnwrapError{}
			}
		}
		for _, child := range normalized.children {
			if child, ok := child.(*normalizedError); ok { //nolint:errorlint // Never traverse an untrusted child here.
				queue = append(queue, child)
			}
		}
	}
	return failures
}

func detailFailures(err error, ids []model.TicketID) (map[model.TicketID]error, error) {
	err = NormalizeError(err)
	bounded := oversizedFailureMembership(err, ids)
	var typed *provider.DetailFailures
	if !errors.As(err, &typed) {
		return bounded, err
	}
	if typed == nil {
		return bounded, errors.New("provider: FetchDetails returned a typed-nil DetailFailures error")
	}

	// A custom As matcher can synthesize an aggregate that was not part of the
	// graph NormalizeError scanned above. Normalize it before exposing its map.
	safe := NormalizeError(typed)
	for id, failure := range oversizedFailureMembership(safe, ids) {
		if bounded == nil {
			bounded = make(map[model.TicketID]error)
		}
		bounded[id] = failure
	}
	var normalized *provider.DetailFailures
	if !errors.As(safe, &normalized) || normalized == nil {
		return bounded, safe
	}
	if len(bounded) == 0 {
		return normalized.Failures, nil
	}
	failures := make(map[model.TicketID]error, len(normalized.Failures)+len(bounded))
	for id, failure := range normalized.Failures {
		failures[id] = failure
	}
	for id, failure := range bounded {
		if _, exists := failures[id]; !exists {
			failures[id] = failure
		}
	}
	// The bounded aggregate can still cover an incomplete planned ID that no safe
	// aggregate names. Keep its response-wide sentinel while completed siblings
	// remain eligible for success.
	return failures, err
}

// invalidResults turns malformed native-batch output into per-ID failures or a
// deterministic batch error without letting an unrequested key poison a valid ID.
func invalidResults(ids []model.TicketID, details map[model.TicketID]model.Detail, failures map[model.TicketID]error) (map[model.TicketID]error, error) {
	invalid := make(map[model.TicketID]error)
	requested := make(map[model.TicketID]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
		if detail, ok := details[id]; ok && detail.TicketID != id {
			invalid[id] = fmt.Errorf("provider: detail for %q returned TicketID %q", id, detail.TicketID)
		}
		if failure, ok := failures[id]; ok && failure == nil {
			invalid[id] = fmt.Errorf("provider: FetchDetails returned nil failure for %q", id)
		}
		if _, hasDetail := details[id]; hasDetail {
			if _, hasFailure := failures[id]; hasFailure {
				invalid[id] = fmt.Errorf("provider: FetchDetails returned both detail and failure for %q", id)
			}
		}
	}
	keys := make([]string, 0)
	for id := range details {
		if !requested[id] {
			keys = append(keys, string(id))
		}
	}
	for id := range failures {
		if !requested[id] {
			keys = append(keys, string(id))
		}
	}
	if len(keys) != 0 {
		sort.Strings(keys)
		return invalid, fmt.Errorf("provider: FetchDetails returned unrequested TicketID %q", keys[0])
	}
	return invalid, nil
}

// Links folds resolved Details into the map model.BuildBlockingGraph consumes.
// Only a successful fetch writes a key: key presence is the tri-state that makes
// fail-closed Actionable real.
func Links(details map[model.TicketID]model.Detail) map[model.TicketID][]model.Link {
	links := make(map[model.TicketID][]model.Link, len(details))
	for id, d := range details {
		links[id] = d.Links
	}
	return links
}
