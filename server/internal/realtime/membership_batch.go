package realtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	maxMembershipBatch = 256
	membershipWorkers  = 4
)

type membershipWriteRequest struct {
	identity MembershipIdentity
	result   chan bool
	ctx      context.Context
}

func (r membershipWriteRequest) pending() bool {
	return r.ctx == nil || r.ctx.Err() == nil
}

func (r membershipWriteRequest) finish(member bool) {
	r.result <- member && r.pending()
}

func (h *Hub) authorizeWrites(identity MembershipIdentity) bool {
	return h.authorizeWriteIdentities([]MembershipIdentity{identity})[identity]
}

// A socket batch can contain content from several workspaces and groups. Queue
// its distinct checks together under one budget, not one timeout per frame.
func (h *Hub) authorizeWriteIdentities(identities []MembershipIdentity) map[MembershipIdentity]bool {
	h.writeMembershipOnce.Do(func() { go h.runMembershipChecks() })
	ctx, cancel := context.WithTimeout(context.Background(), membershipLookupTimeout+time.Second)
	defer cancel()
	requests := make(map[MembershipIdentity]membershipWriteRequest, len(identities))
	for _, identity := range identities {
		if _, exists := requests[identity]; exists {
			continue
		}
		request := membershipWriteRequest{identity: identity, result: make(chan bool, 1), ctx: ctx}
		select {
		case h.writeMembership <- request:
			requests[identity] = request
		case <-ctx.Done():
			return nil
		}
	}
	members := make(map[MembershipIdentity]bool, len(requests))
	for identity, request := range requests {
		select {
		case member := <-request.result:
			members[identity] = member
		case <-ctx.Done():
			return nil
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return members
}

// Coalesce queued write batches across sockets into one indexed database
// query. Close the request cohort BEFORE starting the query: joining a lookup
// already in flight could authorize newly created content with an old snapshot.
func (h *Hub) runMembershipChecks() {
	// One collector fixes each cohort before passing it to a bounded pool.
	// The unbuffered handoff avoids a second unbounded backlog of SQL jobs.
	jobs := make(chan []membershipWriteRequest)
	var workers sync.WaitGroup
	for i := 0; i < membershipWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for requests := range jobs {
				h.checkWriteMembership(requests)
			}
		}()
	}
	for first := range h.writeMembership {
		if !first.pending() {
			first.finish(false)
			continue
		}
		requests := make([]membershipWriteRequest, 0, maxMembershipBatch)
		requests = append(requests, first)
		timer := time.NewTimer(time.Millisecond)
	collect:
		for len(requests) < maxMembershipBatch {
			select {
			case request, open := <-h.writeMembership:
				if !open {
					break collect
				}
				if !request.pending() {
					request.finish(false)
					continue
				}
				requests = append(requests, request)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		jobs <- requests
	}
	close(jobs)
	workers.Wait()
}

func (h *Hub) checkWriteMembership(requests []membershipWriteRequest) {
	live := requests[:0]
	identities := make([]MembershipIdentity, 0, len(requests))
	seen := make(map[MembershipIdentity]bool, len(requests))
	var latestDeadline time.Time
	allHaveDeadlines := true
	for _, request := range requests {
		if !request.pending() {
			request.finish(false)
			continue
		}
		live = append(live, request)
		if !seen[request.identity] {
			identities = append(identities, request.identity)
			seen[request.identity] = true
		}
		if request.ctx == nil {
			allHaveDeadlines = false
		} else if deadline, ok := request.ctx.Deadline(); !ok {
			allHaveDeadlines = false
		} else if deadline.After(latestDeadline) {
			latestDeadline = deadline
		}
	}
	if len(live) == 0 {
		return
	}
	// A cohort that waited for a worker gets no database time beyond the
	// last live caller's budget. Expired callers never authorize a write.
	deadline := time.Now().Add(membershipLookupTimeout)
	if allHaveDeadlines && latestDeadline.Before(deadline) {
		deadline = latestDeadline
	}
	h.mu.RLock()
	checker := h.membership
	h.mu.RUnlock()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	var members map[MembershipIdentity]bool
	var err error
	if checker == nil {
		err = errors.New("membership checker unavailable")
	} else {
		members, err = checker.Members(ctx, identities)
	}
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	if err != nil {
		slog.Warn("ws batch membership lookup failed", "identities", len(identities), "error", err)
	}
	for _, request := range live {
		request.finish(err == nil && members[request.identity])
	}
}
