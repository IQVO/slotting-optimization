package a2a

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const testAgentURL = "http://slotting-optimization-a2a.warehouse-systems.svc.cluster.local:8093/"

func testCard() *a2a.AgentCard { return Card(Config{AgentURL: testAgentURL, Version: "1.0.0"}) }

// TestCardShape pins the identity, interface and capability surface of the
// Agent Card: the discovery contract other contexts program against.
func TestCardShape(t *testing.T) {
	card := testCard()

	if card.Name != "slotting-optimization" {
		t.Errorf("card name = %q, want slotting-optimization", card.Name)
	}
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("card has %d interfaces, want 1", len(card.SupportedInterfaces))
	}
	iface := card.SupportedInterfaces[0]
	if iface.URL != testAgentURL || iface.ProtocolBinding != a2a.TransportProtocolJSONRPC {
		t.Errorf("interface = %+v, want JSONRPC at the configured agent URL", iface)
	}
	if card.Capabilities.Streaming {
		t.Error("streaming must not be advertised: this is a message-only agent")
	}
	if card.SecuritySchemes != nil {
		t.Error("card must declare no security schemes: the surface is unauthenticated fleet-wide")
	}
	if card.Provider == nil || card.Provider.Org != "IQVO" {
		t.Errorf("provider = %+v, want IQVO", card.Provider)
	}
}

// TestCardSkillsAreCuratedAndTruthful pins the two-skill, READ-ONLY surface
// and proves the card never lies about its own contract: every advertised
// example must parse under the SAME strict decoder the executor uses, name its
// own skill, and carry arguments that skill accepts.
func TestCardSkillsAreCuratedAndTruthful(t *testing.T) {
	want := map[string]bool{skillForwardSlotMap: true, skillSlotPlanReview: true}
	card := testCard()
	if len(card.Skills) != len(want) {
		t.Fatalf("card has %d skills, want exactly %d (curated, goal-shaped, at most 2)", len(card.Skills), len(want))
	}
	for _, s := range card.Skills {
		if !want[s.ID] {
			t.Errorf("unexpected skill %q on the card", s.ID)
			continue
		}
		if s.Name == "" || s.Description == "" || len(s.Examples) == 0 {
			t.Errorf("skill %q lacks a name, description or example", s.ID)
		}
		if !slices.Contains(s.Tags, "read") || slices.Contains(s.Tags, "write") {
			t.Errorf("skill %q must be tagged read (and never write), tags = %v", s.ID, s.Tags)
		}
		for _, ex := range s.Examples {
			var req skillRequest
			if err := decodeStrict([]byte(ex), &req); err != nil {
				t.Errorf("skill %q example is not valid under the strict decoder: %v", s.ID, err)
				continue
			}
			if req.Skill != s.ID {
				t.Errorf("skill %q example names skill %q", s.ID, req.Skill)
			}
			assertArgsAccepted(t, s.ID, req.Arguments)
		}
	}
}

func assertArgsAccepted(t *testing.T, skill string, raw json.RawMessage) {
	t.Helper()
	var err error
	switch skill {
	case skillForwardSlotMap:
		err = decodeStrict(raw, &forwardSlotMapArgs{})
	case skillSlotPlanReview:
		err = decodeStrict(raw, &slotPlanReviewArgs{})
	}
	if err != nil {
		t.Errorf("skill %q example arguments are rejected by its own strict decoder: %v", skill, err)
	}
}

// TestCardIsTruthfullyReadOnly: the description says what the agent will not
// do, and never names a write operation as an offering.
func TestCardIsTruthfullyReadOnly(t *testing.T) {
	card := testCard()
	if !strings.Contains(card.Description, "read-only") || !strings.Contains(card.Description, "REST") {
		t.Errorf("the card must say it is read-only and where the writes live: %q", card.Description)
	}
	for _, s := range card.Skills {
		for _, verb := range writeVerbs {
			for _, word := range strings.Split(s.ID, "_") {
				if word == verb {
					t.Errorf("skill %q carries the write verb %q", s.ID, verb)
				}
			}
		}
	}
}

// TestCardSerializable proves the card marshals to JSON a client can parse
// back, with the discovery fields intact.
func TestCardSerializable(t *testing.T) {
	raw, err := json.Marshal(testCard())
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	var back a2a.AgentCard
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("card JSON does not round-trip: %v", err)
	}
	if back.Name != "slotting-optimization" || len(back.Skills) != 2 {
		t.Errorf("round-tripped card = %q with %d skills", back.Name, len(back.Skills))
	}
}
