# Derived from: docs/contexts/slotting-optimization business-context.md ("The v1 policy: abc-velocity-v1"), ubiquitous-language.md
# (ABC class, Eligibility, Stickiness, Unassigned reasons), use-cases.md ("How GeneratePlan computes a plan"), troubleshooting.md
# ("Plans that come out empty or partial"), aggregate-design-canvas.md (assignments, moves and unassigned stored sorted by SKU)
@bdd
Feature: The abc-velocity-v1 policy - ranking, classes, eligibility and stickiness
  SKUs are ranked by picks, then units, then SKU code; each gets an ABC class
  from the share of picks ranked before it (A below 80 %, B below 95 %, else
  C) and takes the best free eligible slot, preferring the slot it held in the
  site's Approved plan. A slot holds one SKU. Eligible means compatible
  (hazmat, temperature) and a unit that fits the slot's volume and weight.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"

  Scenario Outline: The ABC class follows the share of picks ranked before the SKU
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-03" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has <first> demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has <second> demand lines of 1 units each at site "SITE-1"
    And "SKU-3" has <third> demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And "SKU-3" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    And the plan assigns "SKU-2" to slot "A-02" in class "<class2>"
    And the plan assigns "SKU-3" to slot "A-03" in class "<class3>"

    Examples:
      | first | second | third | class2 | class3 |
      | 79    | 16     | 5     | A      | C      |
      | 80    | 15     | 5     | B      | C      |
      | 80    | 14     | 6     | B      | B      |
      | 94    | 5      | 1     | B      | C      |
      | 95    | 4      | 1     | C      | C      |

  Scenario: The only candidate is always class A
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 1 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: Equal picks are broken by units: the SKU with more units gets the only slot
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-A" has 4 demand lines of 5 units each at site "SITE-1"
    And "SKU-B" has 4 demand lines of 10 units each at site "SITE-1"
    And "SKU-A" has an effective size of 1000 mm3 and 10 g
    And "SKU-B" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-B" to slot "A-01" in class "A"
    And "SKU-A" is unassigned because "NoEligibleSlot"

  Scenario: Equal picks and units are broken by SKU code
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-B" has 4 demand lines of 5 units each at site "SITE-1"
    And "SKU-A" has 4 demand lines of 5 units each at site "SITE-1"
    And "SKU-A" has an effective size of 1000 mm3 and 10 g
    And "SKU-B" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-A" to slot "A-01" in class "A"
    And "SKU-B" is unassigned because "NoEligibleSlot"

  Scenario: A SKU that fits only one slot finds it taken by a better-ranked SKU: NoEligibleSlot
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 1 kg and 0.0005 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has an effective size of 1000000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    And "SKU-2" is unassigned because "NoEligibleSlot"

  Scenario: NoCapacityFit is decided on size alone, whether or not the slots are taken
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 1 kg and 0.001 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has an effective size of 1000000000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    And "SKU-2" is unassigned because "NoCapacityFit"

  Scenario Outline: A unit that exactly fills the slot fits
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of <volume> mm3 and <weight> g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

    Examples:
      | volume    | weight |
      | 500000000 | 10     |
      | 1000      | 40000  |
      | 500000000 | 40000  |

  Scenario Outline: A unit one step over the slot's volume or weight does not fit
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of <volume> mm3 and <weight> g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoCapacityFit"

    Examples:
      | volume    | weight |
      | 500000001 | 10     |
      | 1000      | 40001  |

  Scenario Outline: Hazmat and temperature class decide compatibility, an unclassified SKU is Ambient
    Given the <kind> zone "Z-X" of site "SITE-1" with code "FWD" is registered with temperature "<zone temperature>"
    And the slot "X-01" in zone "Z-X" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" is classified with tags "<tags>" and temperature "<sku temperature>"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "<slot>" in class "A"

    Examples:
      | kind   | zone temperature | tags           | sku temperature | slot |
      | plain  | Ambient          |                |                 | X-01 |
      | plain  | Ambient          | Fragile        | Ambient         | X-01 |
      | hazmat | Ambient          | Hazmat         |                 | X-01 |
      | hazmat | Ambient          | Fragile,Hazmat | Ambient         | X-01 |
      | hazmat | Frozen           | Hazmat         | Frozen          | X-01 |
      | plain  | Frozen           |                | Frozen          | X-01 |

  Scenario Outline: A SKU is refused every slot of a zone whose hazmat flag or temperature differs
    Given the <kind> zone "Z-X" of site "SITE-1" with code "FWD" is registered with temperature "<zone temperature>"
    And the slot "X-01" in zone "Z-X" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" is classified with tags "<tags>" and temperature "<sku temperature>"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"

    Examples:
      | kind   | zone temperature | tags    | sku temperature |
      | plain  | Chilled          |         |                 |
      | plain  | Frozen           |         | Ambient         |
      | plain  | Ambient          |         | Chilled         |
      | plain  | Chilled          |         | Frozen          |
      | plain  | Ambient          | Hazmat  | Ambient         |
      | hazmat | Ambient          |         |                 |
      | hazmat | Ambient          | Fragile | Ambient         |
      | hazmat | Chilled          | Hazmat  | Ambient         |

  Scenario: A SKU keeps its previous slot when a better-ranked slot is free
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 1 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 1
    And the 3 demand lines of "SKU-1" at site "SITE-1" are removed
    When I generate a slot plan
    Then the plan assigns "SKU-2" to slot "A-02" in class "A"
    And the plan has 1 moves
    And the plan has a "Vacate" move of "SKU-1" from "A-01"

  Scenario: A vacated SKU's slot is free for a newcomer in the same plan
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 1
    And the 3 demand lines of "SKU-1" at site "SITE-1" are removed
    And "SKU-2" has 2 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-2" to slot "A-01" in class "A"
    And the plan has a "Assign" move of "SKU-2" to "A-01"
    And the plan has a "Vacate" move of "SKU-1" from "A-01"
    And the plan has 2 moves

  Scenario: A previous slot that was decommissioned is not kept: the SKU takes the best free slot
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-03" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 1
    And the slot "A-01" is decommissioned
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-02" in class "A"
    And the plan assigns "SKU-2" to slot "A-03" in class "A"
    And the plan has a "Relocate" move of "SKU-1" from "A-01" to "A-02"
    And the plan has a "Relocate" move of "SKU-2" from "A-02" to "A-03"

  Scenario: A previous slot the SKU no longer fits is given up
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 1
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.0000005 m3
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-02" in class "A"
    And the plan has 1 moves
    And the plan has a "Relocate" move of "SKU-1" from "A-01" to "A-02"

  Scenario: The next plan is computed against the Approved plan, not against a Draft or a Rejected plan
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I reject plan number 1 with reason "Not now."
    When I generate a slot plan
    Then the plan has a "Assign" move of "SKU-1" to "A-01"
    And the plan has 1 moves

  Scenario: Assignments, moves and unassigned entries are listed sorted by SKU, and the event counts them
    Given the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-C" has 6 demand lines of 1 units each at site "SITE-1"
    And "SKU-B" has 4 demand lines of 1 units each at site "SITE-1"
    And "SKU-A" has 2 demand lines of 1 units each at site "SITE-1"
    And "SKU-D" has 1 demand lines of 1 units each at site "SITE-1"
    And "SKU-A" has an effective size of 1000 mm3 and 10 g
    And "SKU-B" has an effective size of 1000 mm3 and 10 g
    And "SKU-C" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the response list "assignments" names the SKUs "SKU-B, SKU-C" in that order
    And the response list "moves" names the SKUs "SKU-B, SKU-C" in that order
    And the response list "unassigned" names the SKUs "SKU-A, SKU-D" in that order
    And "SKU-A" is unassigned because "NoEligibleSlot"
    And "SKU-D" is unassigned because "NoPhysicalProfile"
    When I read the "SlotPlanGenerated" event of plan number 1
    Then the event field "data.assignment_count" is "2"
    And the event field "data.move_count" is "2"
    And the event field "data.unassigned_count" is "2"
