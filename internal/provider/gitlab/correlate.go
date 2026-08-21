package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/niekcandaele/sitrep/internal/model"
)

// mergeRequestWorkers bounds how many closed_by requests are in flight at
// once.
//
// Serial is unusable: a forty-Ticket epic at a sixty-second poll would spend
// most of its interval waiting on round trips. Unbounded is how a polling tool
// gets itself rate-limited, and on a self-managed instance it looks like an
// attack. Eight keeps a large epic inside a couple of round trips and never
// looks like either.
//
// The pool is hand-rolled from a channel and a sync.WaitGroup rather than
// golang.org/x/sync/errgroup on purpose: errgroup is an indirect dependency
// today and using it would promote it to a direct one, which ADR-0001 forbids.
const mergeRequestWorkers = 8

// correlate attaches the merge requests moving each Ticket, and refines each
// Ticket's Status Category with them. It is the only place FetchEpic makes more
// than one kind of request per node, and it is what Capabilities().PullRequests
// declares.
//
// # Degrade per Ticket, fail as a whole only for systemic problems
//
// A 403 or 404 from one Ticket's closed_by means that one issue's
// project is not fully visible to this token, or has merge requests disabled:
// that Ticket records no merge requests and the snapshot carries on. Anything
// else — 401, 429, 5xx, a transport failure, a decode failure — will hit every
// Ticket in the same way, so it fails the whole FetchEpic with the message
// checkStatus already wrote. Half a situation report about a visibility gap is
// worth having; half a report about an expired token is not.
//
// The fan-out is deterministic. Each worker writes only tickets[i], so
// FetchEpic's Ticket order is untouched and two consecutive fetches return equal
// snapshots; errors are collected by index and scanned in index order, so the
// error a caller sees never depends on scheduling.
func (p *Provider) correlate(ctx context.Context, tickets []model.Ticket) error {
	if len(tickets) == 0 {
		return nil
	}

	indices := make(chan int, len(tickets))
	for i := range tickets {
		indices <- i
	}
	close(indices)

	errs := make([]error, len(tickets))
	var wg sync.WaitGroup
	for range min(len(tickets), mergeRequestWorkers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				// Cancellation is checked before every request as well as being
				// carried by it: the TUI's refresh and cli.RunWith's SIGINT both
				// depend on a cancelled fetch stopping promptly.
				if err := ctx.Err(); err != nil {
					errs[i] = err
					return
				}
				errs[i] = p.correlateTicket(ctx, &tickets[i])
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// correlateTicket attaches one Ticket's merge requests and refines its Status
// Category. A Ticket whose id does not name an issue is skipped rather than
// failed: there is no such Ticket today, and being total here is cheaper than
// being surprised later.
func (p *Provider) correlateTicket(ctx context.Context, ticket *model.Ticket) error {
	t, err := parseTicketID(ticket.ID)
	if err != nil || t.kind != kindIssue {
		return nil
	}

	prs, err := p.mergeRequestsFor(ctx, t)
	if err != nil {
		return err
	}
	ticket.PullRequests = prs
	ticket.Status = statusWithMergeRequests(ticket.Status, prs)
	return nil
}

// mergeRequestsFor reads the merge requests that will close one issue, lead
// first.
//
// Which merge requests those are comes from /closed_by, GitLab's own closing
// linkage. What each of them looks like comes from /related_merge_requests,
// because the two endpoints serialize a merge request differently: only the
// wider list carries head_pipeline, and without it the Checks half of the
// PullRequests Capability goes dark. (Verified against gitlab.com on
// 2026-08-21: closed_by omits head_pipeline; related_merge_requests carries
// it.) So closed_by decides membership and the related payload supplies the
// pipeline for the merge requests it named.
//
// An issue nothing is closing — the common case on a Todo ticket — costs one
// request: the second is skipped when closed_by is empty.
func (p *Provider) mergeRequestsFor(ctx context.Context, t target) ([]model.PullRequest, error) {
	query := url.Values{"per_page": {strconv.Itoa(pageSize)}}

	var mrs []mergeRequestWire
	if _, err := p.do(ctx, t.closedByPath(), query, t.String(), &mrs); err != nil {
		if isTicketScopedFailure(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	if err := p.attachPipelines(ctx, t, query, mrs); err != nil {
		return nil, err
	}

	prs := newPullRequests(mrs)
	if lead, ok := leadIndex(prs); ok {
		approvals, err := p.approvalsFor(ctx, mrs[lead], prs[lead])
		if err != nil {
			return nil, err
		}
		if approvals != nil {
			prs[lead].Review = normalizeReview(
				mrs[lead].DetailedMergeStatus, len(mrs[lead].Reviewers), approvals)
		}
	}
	return leadFirst(prs), nil
}

// attachPipelines fills in the head pipeline closed_by does not serialize,
// reading it from the wider related_merge_requests list and matching on the
// merge request's own id. A merge request the wider list does not mention keeps
// no pipeline, which reads as ChecksNone — the same answer as a project running
// no CI, and the honest one when nothing said otherwise.
func (p *Provider) attachPipelines(ctx context.Context, t target, query url.Values, mrs []mergeRequestWire) error {
	var related []mergeRequestWire
	if _, err := p.do(ctx, t.relatedMergeRequestsPath(), query, t.String(), &related); err != nil {
		if isTicketScopedFailure(err) {
			return nil
		}
		return err
	}

	pipelines := make(map[int]*pipelineWire, len(related))
	for _, m := range related {
		if m.HeadPipeline != nil {
			pipelines[m.ID] = m.HeadPipeline
		}
	}
	for i := range mrs {
		if mrs[i].HeadPipeline == nil {
			mrs[i].HeadPipeline = pipelines[mrs[i].ID]
		}
	}
	return nil
}

// approvalsFor reads the lead merge request's approvals, or returns nil when
// there is nothing to learn.
//
// The call is lead-only, live-only and short-circuited, which bounds the extra
// cost at one request per Ticket. It exists because detailed_merge_status can
// say requested_changes and not_approved but can never say *approved* — a merge
// request that has been approved simply stops saying not_approved — so without
// it ReviewApproved would be unreachable, a visible hole in telling coding from
// waiting-on-review.
//
// A 403 or 404 is swallowed and reported as no approvals: that is the Free-tier
// or restricted-project case, where the endpoint is legitimately unreadable and
// normalizeReview falls through to Pending or None, leaving the report one
// nuance poorer rather than absent. Anything else — 401, 429, 5xx, a transport
// or decode failure — is the same systemic problem for every Ticket, so it
// fails the fetch instead of being laundered into a review state, which is the
// distinction correlate's doc comment already draws.
func (p *Provider) approvalsFor(ctx context.Context, m mergeRequestWire, pr model.PullRequest) (*approvalsWire, error) {
	if pr.State != model.PROpen && pr.State != model.PRDraft {
		// A merged or closed merge request's review posture is history, and its
		// detailed_merge_status reads not_open.
		return nil, nil
	}
	if pr.Review == model.ReviewChangesRequested {
		// GitLab has already answered the question, and the answer outranks an
		// approval anyway.
		return nil, nil
	}
	project := m.projectPath()
	if project == "" || m.IID <= 0 {
		return nil, nil
	}

	t := target{kind: kindIssue, path: project, iid: m.IID}
	var approvals approvalsWire
	if _, err := p.do(ctx, t.mergeRequestApprovalsPath(m.IID), nil, t.String(), &approvals); err != nil {
		if isTicketScopedFailure(err) {
			return nil, nil
		}
		return nil, err
	}
	return &approvals, nil
}

// isTicketScopedFailure reports whether an error is one Ticket's problem rather
// than the whole fetch's: a project this token cannot fully see, or one with
// merge requests disabled.
func isTicketScopedFailure(err error) bool {
	var status *statusError
	if !errors.As(err, &status) {
		return false
	}
	return status.status == http.StatusForbidden || status.status == http.StatusNotFound
}
