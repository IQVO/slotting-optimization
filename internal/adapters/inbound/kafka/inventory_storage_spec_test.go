package kafka

import (
	"errors"
	"testing"
)

// Pinned-contract tests for the inventory-storage edge. The pinned copy is
// byte-equal to IQVO/inventory-storage develop (contracts/pinned.json).
// Fixtures: the producer's own message examples (provenance: spec).
const inventoryStoragePin = "testdata/inventory-storage.asyncapi.yaml"

// E1: the one type the relocation consumer dispatches on (stock_consumer.go)
// stays documented on StockTopic in the producer's pinned catalog.
func TestInventoryStoragePinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, inventoryStoragePin)
	requirePinnedDefaultContentType(t, doc)
	types := pinnedChannelTypes(t, doc, StockTopic)
	key, ok := types[TypeStockRelocated]
	if !ok {
		t.Fatalf("pinned inventory-storage spec no longer documents %s on %s", TypeStockRelocated, StockTopic)
	}
	requirePinnedMessageContentType(t, doc, key)
}

// E2: the spec's own StockRelocated examples decode through the REAL
// relocation consumer, route to exactly their own handler, and dedupe on id.
func TestInventoryStorageGoldenEventsDecode(t *testing.T) {
	doc := loadPinnedSpec(t, inventoryStoragePin)
	cp := newPinnedSpecCopies()
	goldens := pinnedGoldenFlow(t, cp.stock, doc, StockTopic, []string{TypeStockRelocated}, cp.rec)
	pinnedReplayNoNewClaims(t, cp.stock, goldens, cp.claims)
}

// E3: every envelope invariant, and every const the pin narrows, holds on
// the golden examples.
func TestInventoryStorageEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, inventoryStoragePin)
	pinnedEnvelopeConsts(t, doc, StockTopic, TypeStockRelocated)
}

// E4: unknown data keys are tolerated (and still applied); a documented
// but unhandled inventory-storage type is skipped; a non-CloudEvent body is
// skipped; and — this consumer's OWN policy (ADR 0010, DeadLetterInvalid) —
// a StockRelocated whose payload can NEVER be applied is marked poison, not
// silently skipped.
func TestInventoryStorageTolerancePolicy(t *testing.T) {
	doc := loadPinnedSpec(t, inventoryStoragePin)
	cp := newPinnedSpecCopies()
	pinnedTolerancePolicy(t, cp.stock, doc, StockTopic, map[string]bool{TypeStockRelocated: true}, cp.rec)

	// The repo's documented poison policy: an undecodable payload of a
	// HANDLED type dead-letters (errPoison), it is never skipped.
	poison := map[string]any{
		"specversion": "1.0", "id": "pin-poison-1", "source": "/warehouse/inventory-storage",
		"type": TypeStockRelocated, "subject": "rel-x", "time": "2026-10-17T12:00:00Z",
		"datacontenttype": "application/json",
		"dataschema":      "urn:warehouse:inventory-storage:events:StockRelocated:v1",
		"data":            "a string can never decode into the payload struct",
	}
	err := cp.stock.HandleMessage(t.Context(), pinnedEventBytes(t, poison))
	if !errors.Is(err, errPoison) {
		t.Fatalf("unusable StockRelocated payload must be poison (dead-letter), got %v", err)
	}
}
