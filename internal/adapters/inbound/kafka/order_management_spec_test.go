package kafka

import (
	"testing"
)

// Pinned-contract tests for the order-management edge. The pinned copy is
// byte-equal to IQVO/order-management develop (contracts/pinned.json).
// Fixtures: the producer's own message examples (provenance: spec).
const orderManagementPin = "testdata/order-management.asyncapi.yaml"

// E1: the one type the demand consumer dispatches on (demand_consumer.go)
// stays documented on DemandTopic in the producer's pinned catalog.
func TestOrderManagementPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPin)
	requirePinnedDefaultContentType(t, doc)
	types := pinnedChannelTypes(t, doc, DemandTopic)
	key, ok := types[TypeSiteSkuDemandChanged]
	if !ok {
		t.Fatalf("pinned order-management spec no longer documents %s on %s", TypeSiteSkuDemandChanged, DemandTopic)
	}
	requirePinnedMessageContentType(t, doc, key)
}

// E2: the spec's own SiteSkuDemandChanged examples decode through the REAL
// demand consumer, route to exactly their own handler, and dedupe on id.
func TestOrderManagementGoldenEventsDecode(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPin)
	cp := newPinnedSpecCopies()
	goldens := pinnedGoldenFlow(t, cp.demand, doc, DemandTopic, []string{TypeSiteSkuDemandChanged}, cp.rec)
	pinnedReplayNoNewClaims(t, cp.demand, goldens, cp.claims)
}

// E3: every envelope invariant, and every const the pin narrows (the
// subject pattern <order>/line/<n> is additionally narrowed by the pin),
// holds on the golden examples.
func TestOrderManagementEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPin)
	pinnedEnvelopeConsts(t, doc, DemandTopic, TypeSiteSkuDemandChanged)
	for _, ex := range pinnedExamples(t, doc, DemandTopic, TypeSiteSkuDemandChanged) {
		if subject := strVal(ex["subject"]); subject == "" || !containsLine(subject) {
			t.Errorf("SiteSkuDemandChanged subject %q is not <order_id>/line/<line_no>", subject)
		}
	}
}

func containsLine(subject string) bool {
	for i := 0; i+6 <= len(subject); i++ {
		if subject[i:i+6] == "/line/" {
			return true
		}
	}
	return false
}

// E4: unknown data keys are tolerated (and still applied); a documented
// but unhandled order-management type (e.g. OrderAllocated) is skipped; a
// non-CloudEvent body is skipped — the consumer's documented policy.
func TestOrderManagementTolerancePolicy(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPin)
	cp := newPinnedSpecCopies()
	pinnedTolerancePolicy(t, cp.demand, doc, DemandTopic, map[string]bool{TypeSiteSkuDemandChanged: true}, cp.rec)
}
