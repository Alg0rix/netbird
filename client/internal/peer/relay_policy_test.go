package peer

import (
	"bytes"
	"context"
	"testing"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	sproto "github.com/netbirdio/netbird/shared/signal/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedPolicyRejectsUnmatchedInstructions(t *testing.T) {
	p := &rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Digest: "desired", CatalogGeneration: 4, ReselectionGeneration: 8, Tiers: [][]rp.RelayCatalogEntry{{{ID: "a"}}}}
	m := newManagedRelay(&WorkerRelay{config: ConnConfig{RelayPolicy: p}})
	offer := m.announcement()
	require.True(t, m.accept(offer), "matching policy announcement must permit negotiation")
	for _, mutate := range []func(*sproto.RelayPolicyAttempt){
		func(a *sproto.RelayPolicyAttempt) { a.Digest = "stale" },
		func(a *sproto.RelayPolicyAttempt) { a.CatalogGeneration-- },
		func(a *sproto.RelayPolicyAttempt) { a.ReselectionGeneration-- },
		func(a *sproto.RelayPolicyAttempt) { a.Phase = 99 },
	} {
		stale := cloneAttempt(offer)
		mutate(stale)
		assert.False(t, m.accept(stale), "stale or unknown instructions must never establish a path")
	}
	assert.False(t, m.accept(nil), "managed policy must reject legacy signaling")
}

func TestManagedBlockedPolicyPreservesDirectSignaling(t *testing.T) {
	p := &rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked, Digest: "empty", CatalogGeneration: 4}
	m := newManagedRelay(&WorkerRelay{config: ConnConfig{RelayPolicy: p}})
	assert.True(t, m.accept(m.announcement()), "empty relay intersection must retain direct negotiation")
	assert.False(t, m.readyPolicy(), "empty relay intersection cannot establish native relay")
	p.ForceRelay = true
	assert.False(t, m.accept(m.announcement()), "force relay with an empty intersection must deny signaling")
}

func TestManagedResponderFencesPriorIncarnations(t *testing.T) {
	p := &rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Digest: "desired", Tiers: [][]rp.RelayCatalogEntry{{{ID: "a"}}}}
	m := newManagedRelay(&WorkerRelay{peerCtx: context.Background(), config: ConnConfig{RelayPolicy: p}})
	first := m.announcement()
	first.Nonce = bytes.Repeat([]byte{1}, 16)
	m.receive(&OfferAnswer{RelayPolicyAttempt: first})
	second := cloneAttempt(first)
	second.Nonce = bytes.Repeat([]byte{2}, 16)
	m.receive(&OfferAnswer{RelayPolicyAttempt: second})
	m.receive(&OfferAnswer{RelayPolicyAttempt: first})
	assert.Equal(t, second.Nonce, m.remoteNonce, "retired controller announcements cannot replace the current incarnation")
	stale := cloneAttempt(second)
	stale.Sequence = 1
	stale.RelayId = "a"
	stale.ResponderNonce = bytes.Repeat([]byte{3}, 16)
	m.receive(&OfferAnswer{RelayPolicyAttempt: stale})
	assert.Nil(t, m.current, "an offer aimed at a previous responder incarnation cannot start acquisition")
	stale.ResponderNonce = append([]byte(nil), m.nonce...)
	stale.Nonce = first.Nonce
	m.receive(&OfferAnswer{RelayPolicyAttempt: stale})
	assert.Nil(t, m.current, "an old controller nonce cannot replace the latest controller incarnation")
}

func TestManagedAttemptRequiresBothNoncesAndConcreteIdentity(t *testing.T) {
	a := &sproto.RelayPolicyAttempt{Digest: "d", CatalogGeneration: 1, ReselectionGeneration: 2, Nonce: bytes.Repeat([]byte{1}, 16), ResponderNonce: bytes.Repeat([]byte{2}, 16), Sequence: 3, RelayId: "a", Instance: "rels://relay.example:443"}
	b := cloneAttempt(a)
	b.Phase = 1
	assert.True(t, sameAttempt(a, b), "acknowledgment phase must preserve the attempt identity")
	for _, mutate := range []func(*sproto.RelayPolicyAttempt){
		func(b *sproto.RelayPolicyAttempt) { b.ResponderNonce[0]++ },
		func(b *sproto.RelayPolicyAttempt) { b.Nonce[0]++ },
		func(b *sproto.RelayPolicyAttempt) { b.Sequence++ },
		func(b *sproto.RelayPolicyAttempt) { b.RelayId = "b" },
		func(b *sproto.RelayPolicyAttempt) { b.Instance = "rels://other.example:443" },
	} {
		b := cloneAttempt(a)
		mutate(b)
		assert.False(t, sameAttempt(a, b), "an acknowledgment from another attempt cannot install a path")
	}
}
