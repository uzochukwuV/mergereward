package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

type BountyStatus string

const (
	BountyStatusPendingPayment BountyStatus = "pending_payment"
	BountyStatusFunded         BountyStatus = "funded"
	BountyStatusPaid           BountyStatus = "paid"
)

type Bounty struct {
	ID              string
	RepoID          string
	IssueNumber     int
	AmountCents     int64
	Currency        string
	StripeCheckoutID string
	Status          BountyStatus
	CreatedAt       time.Time
	MergedPR        *MergedPR
	AI             *AIResult
}

type MergedPR struct {
	RepoID   string
	PRNumber int
	SHA      string
	Author   string
	MergedAt time.Time
	Body     string
}

type AIResult struct {
	Score   int
	Summary string
}

type Memory struct {
	mu      sync.RWMutex
	bounties map[string]*Bounty
}

func NewMemory() *Memory {
	return &Memory{bounties: map[string]*Bounty{}}
}

func (m *Memory) CreateBounty(b *Bounty) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.ID = newID("bty")
	m.bounties[b.ID] = b
}

func (m *Memory) AttachStripeCheckout(bountyID, checkoutID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bounties[bountyID]
	if !ok {
		return ErrNotFound
	}
	b.StripeCheckoutID = checkoutID
	return nil
}

func (m *Memory) SetBountyStatus(bountyID string, st BountyStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bounties[bountyID]
	if !ok {
		return ErrNotFound
	}
	b.Status = st
	return nil
}

func (m *Memory) FindBountyByRepoIssue(repoID string, issueNumber int) (*Bounty, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.bounties {
		if b.RepoID == repoID && b.IssueNumber == issueNumber {
			return b, true
		}
	}
	return nil, false
}

func (m *Memory) RecordPRMerge(bountyID string, pr MergedPR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bounties[bountyID]
	if !ok {
		return ErrNotFound
	}
	b.MergedPR = &pr
	return nil
}

func (m *Memory) SetAIResult(bountyID string, res AIResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bounties[bountyID]
	if !ok {
		return ErrNotFound
	}
	b.AI = &res
	return nil
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
