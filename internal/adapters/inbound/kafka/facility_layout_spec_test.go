package kafka

import (
	"testing"
)

// Pinned-contract tests for the facility-layout edge. The pinned copy is
// byte-equal to IQVO/facility-layout develop (contracts/pinned.json).
// Fixtures: the producer's own message examples (provenance: spec).
const facilityLayoutPin = "testdata/facility-layout.asyncapi.yaml"

var facilityLayoutHandled = []string{TypeZoneRegistered, TypeLocationSlotRegistered, TypeLocationSlotDecommissioned}

// E1: every type the layout consumer dispatches on (layout_consumer.go)
// stays documented on LayoutTopic in the producer's pinned catalog.
func TestFacilityLayoutPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, facilityLayoutPin)
	requirePinnedDefaultContentType(t, doc)
	types := pinnedChannelTypes(t, doc, LayoutTopic)
	for _, typ := range facilityLayoutHandled {
		key, ok := types[typ]
		if !ok {
			t.Fatalf("pinned facility-layout spec no longer documents %s on %s", typ, LayoutTopic)
		}
		requirePinnedMessageContentType(t, doc, key)
	}
}

// E2: the spec's own golden examples decode through the REAL layout
// consumer, route to exactly their own handler, and dedupe on id.
func TestFacilityLayoutGoldenEventsDecode(t *testing.T) {
	doc := loadPinnedSpec(t, facilityLayoutPin)
	cp := newPinnedSpecCopies()
	goldens := pinnedGoldenFlow(t, cp.layout, doc, LayoutTopic, facilityLayoutHandled, cp.rec)
	pinnedReplayNoNewClaims(t, cp.layout, goldens, cp.claims)
}

// E3: every envelope invariant, and every const the pin narrows (type,
// source, dataschema, datacontenttype), holds on the golden examples.
func TestFacilityLayoutEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, facilityLayoutPin)
	for _, typ := range facilityLayoutHandled {
		pinnedEnvelopeConsts(t, doc, LayoutTopic, typ)
	}
}

// E4: unknown data keys are tolerated (and still applied); a documented
// but unhandled facility-layout type is skipped; a non-CloudEvent body is
// skipped — the consumer's documented policy (consumer.go).
func TestFacilityLayoutTolerancePolicy(t *testing.T) {
	doc := loadPinnedSpec(t, facilityLayoutPin)
	cp := newPinnedSpecCopies()
	handled := map[string]bool{}
	for _, typ := range facilityLayoutHandled {
		handled[typ] = true
	}
	pinnedTolerancePolicy(t, cp.layout, doc, LayoutTopic, handled, cp.rec)
}
