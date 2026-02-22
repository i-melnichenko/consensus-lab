package raft

import (
	"context"
	"math/rand"
	"time"
)

func (n *Node) runFollower(ctx context.Context) {
	timer := n.newTimer(n.electionTimeoutFn())
	defer timer.Stop()

	resetCh := n.electionTimeoutResetSignal()

	for {
		select {
		case <-ctx.Done():
			return
		case <-resetCh:
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(n.electionTimeoutFn())
		case <-timer.C():
			n.mu.Lock()
			n.logger.Debug("election timeout fired, converting to candidate",
				"node_id", n.id,
				"term", n.currentTerm,
			)
			n.role = Candidate
			n.mu.Unlock()
			return
		}
	}
}

func (n *Node) runCandidate(ctx context.Context) {
	n.mu.Lock()
	prevTerm := n.currentTerm
	prevVotedFor := n.votedFor
	n.currentTerm++
	term := n.currentTerm
	n.votedFor = n.id
	if err := n.persistHardStateLocked(); err != nil {
		n.markDegradedLocked(err)
		n.currentTerm = prevTerm
		n.votedFor = prevVotedFor
		n.role = Follower
		n.mu.Unlock()
		return
	}
	lastLogIndex := n.lastLogIndexLocked()
	lastLogTerm := n.lastLogTermLocked()
	n.mu.Unlock()

	n.logger.Debug("starting election",
		"node_id", n.id,
		"term", term,
		"last_log_index", lastLogIndex,
		"last_log_term", lastLogTerm,
		"peers", len(n.peers),
	)

	votes := 1
	majority := n.quorumSize()

	timer := n.newTimer(n.electionTimeoutFn())
	defer timer.Stop()

	voteCh := make(chan *RequestVoteResponse, len(n.peers))

	for peerID, peerClient := range n.peers {
		go func(id string, pc PeerClient) {
			n.logger.Debug("requesting vote",
				"node_id", n.id,
				"term", term,
				"peer", id,
			)

			req := &RequestVoteRequest{
				Term:         term,
				CandidateID:  n.id,
				LastLogIndex: lastLogIndex,
				LastLogTerm:  lastLogTerm,
			}

			resp, err := pc.RequestVote(ctx, req)
			if err != nil {
				n.logger.Debug("vote request failed",
					"node_id", n.id,
					"term", term,
					"peer", id,
					"error", err,
				)
				return
			}

			select {
			case voteCh <- resp:
			case <-ctx.Done():
			}
		}(peerID, peerClient)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
			n.logger.Debug("election timed out, restarting",
				"node_id", n.id,
				"term", term,
			)
			return
		case resp := <-voteCh:

			n.mu.Lock()
			if resp.Term > n.currentTerm {
				n.logger.Debug("stepping down: higher term seen during election",
					"node_id", n.id,
					"current_term", n.currentTerm,
					"peer_term", resp.Term,
				)
				n.currentTerm = resp.Term
				n.votedFor = ""
				n.role = Follower
				if err := n.persistHardStateLocked(); err != nil {
					n.markDegradedLocked(err)
				}
				n.mu.Unlock()
				return
			}
			if n.role != Candidate {
				n.mu.Unlock()
				return
			}

			if resp.VoteGranted {
				votes++
				n.logger.Debug("vote granted",
					"node_id", n.id,
					"term", term,
					"votes", votes,
					"majority", majority,
				)
			} else {
				n.logger.Debug("vote denied",
					"node_id", n.id,
					"term", term,
				)
			}

			if votes >= majority {
				n.logger.Debug("won election, becoming leader",
					"node_id", n.id,
					"term", term,
					"votes", votes,
				)
				n.role = Leader
				n.nextIndex[n.id] = n.lastLogIndexLocked() + 1
				n.matchIndex[n.id] = 0
				for peerID := range n.peers {
					n.nextIndex[peerID] = n.lastLogIndexLocked() + 1
					n.matchIndex[peerID] = 0
				}

				n.mu.Unlock()
				return
			}

			n.mu.Unlock()
		}
	}
}

func randomElectionTimeout() time.Duration {
	//nolint:gosec // Raft election timeout requires pseudo-random jitter, not cryptographic randomness.
	return time.Duration(150+rand.Intn(150)) * time.Millisecond
}
