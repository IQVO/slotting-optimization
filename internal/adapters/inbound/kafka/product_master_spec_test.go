package kafka

import (
	"testing"
)

// Pinned-contract tests for the product-master edge. The pinned copy is
// byte-equal to IQVO/product-master develop (contracts/pinned.json).
// Fixtures: the producer's own message examples (provenance: spec).
const productMasterPin = "testdata/product-master.asyncapi.yaml"

var (
	productMasterHandled = []string{TypeProductClassified, TypeProductDimensionsDeclared, TypeProductMeasured}
	packLevelHandled     = []string{TypeProductPackLevelDeclared, TypeProductPackLevelRetired, TypeProductPackLevelMeasured}
)

// E1: every type the product and pack-level consumers dispatch on
// (product_consumer.go, packlevel_consumer.go) stays documented on
// ProductTopic in the producer's pinned catalog.
func TestProductMasterPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPin)
	requirePinnedDefaultContentType(t, doc)
	types := pinnedChannelTypes(t, doc, ProductTopic)
	for _, typ := range append(productMasterHandled, packLevelHandled...) {
		key, ok := types[typ]
		if !ok {
			t.Fatalf("pinned product-master spec no longer documents %s on %s", typ, ProductTopic)
		}
		requirePinnedMessageContentType(t, doc, key)
	}
}

// E2: the spec's own examples of all six consumed types decode through the
// REAL product and pack-level consumers (both read ProductTopic), route to
// exactly their own handler, and dedupe on id.
func TestProductMasterGoldenEventsDecode(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPin)
	cp := newPinnedSpecCopies()
	productGoldens := pinnedGoldenFlow(t, cp.product, doc, ProductTopic, productMasterHandled, cp.rec)
	packGoldens := pinnedGoldenFlow(t, cp.packs, doc, ProductTopic, packLevelHandled, cp.rec)
	pinnedReplayNoNewClaims(t, cp.product, productGoldens, cp.claims)
	pinnedReplayNoNewClaims(t, cp.packs, packGoldens, cp.claims)
}

// E3: every envelope invariant, and every const the pin narrows (type,
// source, dataschema, datacontenttype), holds on the golden examples.
func TestProductMasterEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPin)
	for _, typ := range append(productMasterHandled, packLevelHandled...) {
		pinnedEnvelopeConsts(t, doc, ProductTopic, typ)
	}
}

// E4: unknown data keys are tolerated (and still applied); a documented
// but unhandled product-master type (e.g. ProductRegistered,
// ProductDescriptionChanged) is skipped; a non-CloudEvent body is skipped —
// the consumers' documented policy.
func TestProductMasterTolerancePolicy(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPin)
	cp := newPinnedSpecCopies()
	pinnedTolerancePolicy(t, cp.product, doc, ProductTopic, typeSet(productMasterHandled), cp.rec)
	pinnedTolerancePolicy(t, cp.packs, doc, ProductTopic, typeSet(packLevelHandled), cp.rec)
}

func typeSet(types []string) map[string]bool {
	set := map[string]bool{}
	for _, typ := range types {
		set[typ] = true
	}
	return set
}
