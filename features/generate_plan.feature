Feature: Generate a slot plan
  A plan is computed from the local copies this service keeps of order demand,
  product profiles and the facility layout. SKUs are ranked by picks, then
  units, then SKU; each takes the best free slot that suits it. Nothing is
  moved until a person approves the plan.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-03" in zone "Z-FWD" holds up to 40 kg and 0.5 m3

  Scenario: The busiest SKU gets the best slot and the plan is a Draft
    Given "SKU-SLOW" has 1 demand lines of 5 units each at site "SITE-1"
    And "SKU-FAST" has 16 demand lines of 5 units each at site "SITE-1"
    And "SKU-MID" has 3 demand lines of 5 units each at site "SITE-1"
    And "SKU-FAST" has an effective size of 1000000 mm3 and 1000 g
    And "SKU-MID" has an effective size of 1000000 mm3 and 1000 g
    And "SKU-SLOW" has an effective size of 1000000 mm3 and 1000 g
    When I generate a slot plan
    Then the response status is 201
    And the response field "state" is "Draft"
    And the response field "policy" is "abc-velocity-v1"
    And the response field "version" is "1"
    And the plan assigns "SKU-FAST" to slot "A-01" in class "A"
    And the plan assigns "SKU-MID" to slot "A-02" in class "B"
    And the plan assigns "SKU-SLOW" to slot "A-03" in class "C"
    And the plan has 3 moves
    And the outbox event types are "SlotPlanGenerated"

  Scenario: Removed lines and other sites' demand do not count
    Given "SKU-1" has 3 demand lines of 2 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000000 mm3 and 1000 g
    And the first demand line of "SKU-1" at site "SITE-1" is removed
    And "SKU-2" has 5 demand lines of 2 units each at site "SITE-2"
    And "SKU-2" has an effective size of 1000000 mm3 and 1000 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    And the plan counts 2 picks and 4 units for "SKU-1"
    And "SKU-2" is not in the plan
    And the plan lists 1 assignments

  Scenario: A plan with no demand is empty but valid
    When I generate a slot plan
    Then the response status is 201
    And the plan lists 0 assignments
    And the plan has 0 moves

  Scenario: A SKU with demand but no physical profile cannot be placed
    Given "SKU-NOPRO" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-NOPRO" is classified with tags "" and temperature "Ambient"
    When I generate a slot plan
    Then "SKU-NOPRO" is unassigned because "NoPhysicalProfile"

  Scenario Outline: Every SKU the planner cannot place says why
    Given "<sku>" has 3 demand lines of 1 units each at site "SITE-1"
    And "<sku>" is classified with tags "<tags>" and temperature "<temperature>"
    And "<sku>" has an effective size of <volume> mm3 and <weight> g
    When I generate a slot plan
    Then "<sku>" is unassigned because "<reason>"

    Examples:
      | sku       | tags   | temperature | volume    | weight | reason         |
      | SKU-COLD  |        | Frozen      | 1000      | 10     | NoEligibleSlot |
      | SKU-ACID  | Hazmat | Ambient     | 1000      | 10     | NoEligibleSlot |
      | SKU-BIG   |        | Ambient     | 900000000 | 10     | NoCapacityFit  |
      | SKU-HEAVY |        | Ambient     | 1000      | 41000  | NoCapacityFit  |

  Scenario: Hazmat stays in hazmat slots and everything else stays out of them
    Given the hazmat zone "Z-HAZ" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-00" in zone "Z-HAZ" holds up to 40 kg and 0.5 m3
    And "SKU-ACID" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-ACID" is classified with tags "Hazmat" and temperature "Ambient"
    And "SKU-ACID" has an effective size of 1000 mm3 and 10 g
    And "SKU-PLAIN" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-PLAIN" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-ACID" to slot "A-00" in class "A"
    And the plan assigns "SKU-PLAIN" to slot "A-01" in class "A"

  Scenario: Chilled SKUs only go to chilled zones
    Given the zone "Z-COLD" of site "SITE-1" with code "FWD" is registered with temperature "Chilled"
    And the slot "C-01" in zone "Z-COLD" holds up to 40 kg and 0.5 m3
    And "SKU-MILK" has 4 demand lines of 1 units each at site "SITE-1"
    And "SKU-MILK" is classified with tags "" and temperature "Chilled"
    And "SKU-MILK" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-MILK" to slot "C-01" in class "A"

  Scenario: Only forward zones offer slots
    Given the zone "Z-BULK" of site "SITE-1" with code "BULK" is registered with temperature "Ambient"
    And the slot "B-01" in zone "Z-BULK" holds up to 40 kg and 0.5 m3
    And the slot "A-01" is decommissioned
    And the slot "A-02" is decommissioned
    And the slot "A-03" is decommissioned
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"

  Scenario: A newer physical profile replaces the older and an older one is ignored
    Given "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 900000000 mm3 and 10 g
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-1" gets a stale physical profile of 900000000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
