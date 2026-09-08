// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/s3util"
)

// Lease is the writer lease stored at meta/sessions/{id}.lock.
type Lease struct {
	Token      string    `json:"token"`
	Host       string    `json:"host"`
	AcquiredAt time.Time `json:"acquired_at"`
	TTL        int64     `json:"ttl"` // seconds
}

const (
	// LeaseRefresh is the heartbeat interval.
	LeaseRefresh = 15 * time.Second
	// LeaseTTL is how long a lease survives without a heartbeat (3x refresh).
	LeaseTTL = 45 * time.Second
)

func (r *Registry) leaseKey(id string) string {
	return fmt.Sprintf("%smeta/sessions/%s.lock", r.prefix, id)
}

// ClaimLease acquires the writer lease for a session, failing with ErrLocked
// if an unexpired lease is held by a different token.
func (r *Registry) ClaimLease(ctx context.Context, id, token, host string) error {
	if err := config.ValidateID("session id", id); err != nil {
		return err
	}
	cur, err := r.loadLease(ctx, id)
	if err != nil {
		return err
	}
	if cur != nil && !cur.expired() && cur.Token != token {
		return fmt.Errorf("%w: held by %s (since %s)", ErrLocked, cur.Host, cur.AcquiredAt.Format(time.RFC3339))
	}
	return r.putLease(ctx, id, token, host)
}

// RenewLease refreshes the lease TTL. It fails with ErrLocked if another
// token took the lease over (split-brain detection).
func (r *Registry) RenewLease(ctx context.Context, id, token, host string) error {
	if err := config.ValidateID("session id", id); err != nil {
		return err
	}
	cur, err := r.loadLease(ctx, id)
	if err != nil {
		return err
	}
	if cur != nil && !cur.expired() && cur.Token != token {
		return fmt.Errorf("%w: lost to %s", ErrLocked, cur.Host)
	}
	return r.putLease(ctx, id, token, host)
}

// ReleaseLease removes the lease if (and only if) it still belongs to token.
func (r *Registry) ReleaseLease(ctx context.Context, id, token string) error {
	if err := config.ValidateID("session id", id); err != nil {
		return err
	}
	cur, err := r.loadLease(ctx, id)
	if err != nil {
		return err
	}
	if cur != nil && cur.Token != token {
		return nil // someone else owns it now; leave it alone
	}
	if err := s3util.Delete(ctx, r.client, r.bucket, r.leaseKey(id)); err != nil {
		return fmt.Errorf("registry: release lease %s: %w", id, err)
	}
	return nil
}

func (l *Lease) expired() bool {
	return time.Now().UTC().After(l.AcquiredAt.Add(time.Duration(l.TTL) * time.Second))
}

func (r *Registry) loadLease(ctx context.Context, id string) (*Lease, error) {
	var l Lease
	found, err := r.getJSON(ctx, r.leaseKey(id), &l)
	if err != nil || !found {
		return nil, err
	}
	return &l, nil
}

func (r *Registry) putLease(ctx context.Context, id, token, host string) error {
	l := Lease{Token: token, Host: host, AcquiredAt: time.Now().UTC(), TTL: int64(LeaseTTL.Seconds())}
	if err := r.putJSON(ctx, r.leaseKey(id), l); err != nil {
		return fmt.Errorf("registry: put lease %s: %w", id, err)
	}
	return nil
}
