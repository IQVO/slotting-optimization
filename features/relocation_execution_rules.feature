# Derived from: docs/contexts/slotting-optimization use-cases.md (RecordRelocation, claim slotting-relocation),
# aggregate-design-canvas.md, business-context.md ("The execution progress"), troubleshooting.md (relocation matched no slot-plan move,
# dead-lettering an event that can never be applied); repo docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work.md
@bdd
Feature: Execution progress of an Approved plan from inventory-storage's StockRelocated
  The moves of an Approved plan run on the floor as WES relocation work and
  inventory-storage reports each one as StockRelocated. This context matches the
  relocation to a move (the move reference "<plan_id>:<move_seq>" first, the
  bins only when there is no reference), records it once per relocation id and
  shows moves executed out of moves total on the plan. Progress is advisory: it
  never changes or gates a plan. A relocation that is no plan move is counted,
  never dead-lettered.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 1

  Scenario: Nothing is executed until inventory-storage reports a relocation
    Then plan number 1 has executed 0 of 2 moves
    And the execution shows no last executed time
    And move 0 of the execution is a "Assign" of "SKU-1" not executed
    And move 1 of the execution is a "Assign" of "SKU-2" not executed

  Scenario: A relocation that names the move executes it with its quantity
    When inventory-storage reports relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the delivery was accepted
    And the relocation was observed as "applied"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" executed with 12 units
    And move 1 of the execution is a "Assign" of "SKU-2" not executed
    And the execution was last at "2026-10-09T08:30:00Z"

  Scenario: The move sequence is the 0-based position in the plan's moves, which are sorted by SKU
    When inventory-storage reports relocation "REL-2" of 5 units of "SKU-2" from bin "R-22" to bin "A-02" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:1"
    Then the relocation was observed as "applied"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" not executed
    And move 1 of the execution is a "Assign" of "SKU-2" executed with 5 units

  Scenario: A redelivered CloudEvent is a duplicate and changes nothing
    Given inventory-storage reports event "evt-A" with relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    When inventory-storage reports event "evt-A" with relocation "REL-1" of 99 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the delivery was accepted
    And the relocation was observed as "duplicate"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" executed with 12 units

  Scenario: The same relocation under a new CloudEvent id cannot count twice
    Given inventory-storage reports event "evt-A" with relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    When inventory-storage reports event "evt-B" with relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the relocation was observed as "duplicate"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" executed with 12 units

  Scenario: Several relocations of one move add up and the move counts once
    Given inventory-storage reports relocation "REL-1" of 7 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0" at "2026-10-09T08:30:00Z"
    When inventory-storage reports relocation "REL-2" of 5 units of "SKU-1" from bin "R-18" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0" at "2026-10-09T09:45:00Z"
    Then the relocation was observed as "applied"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" executed with 12 units
    And move 0 of the execution was last executed at "2026-10-09T09:45:00Z"
    And the execution was last at "2026-10-09T09:45:00Z"

  Scenario: The last executed time is the latest relocation, whatever order they arrive in
    Given inventory-storage reports relocation "REL-1" of 7 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0" at "2026-10-09T11:00:00Z"
    When inventory-storage reports relocation "REL-2" of 5 units of "SKU-2" from bin "R-22" to bin "A-02" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:1" at "2026-10-09T08:00:00Z"
    Then plan number 1 has executed 2 of 2 moves
    And the execution was last at "2026-10-09T11:00:00Z"

  Scenario Outline: Without a move reference the relocation is matched on the SKU and bins of the current plan
    When inventory-storage reports relocation "REL-3" of 12 units of "SKU-1" from bin "<from>" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "<reference>"
    Then the relocation was observed as "applied"
    And plan number 1 has executed 1 of 2 moves
    And move 0 of the execution is a "Assign" of "SKU-1" executed with 12 units

    Examples:
      | from | reference   |
      | R-17 |             |
      | R-31 | TASK-991    |
      | R-17 | plan-0001:x |
      | R-17 | plan-0001   |
      | R-17 | :0          |

  Scenario Outline: A relocation that is no move of any plan is counted as unmatched, never dead-lettered
    When inventory-storage reports relocation "REL-9" of 12 units of "<sku>" from bin "R-17" to bin "A-01" in site "<site>" for reason "<reason>" referencing "<reference>"
    Then the delivery was accepted
    And the relocation was observed as "unmatched" because "<because>"
    And plan number 1 has executed 0 of 2 moves

    Examples:
      | sku   | site   | reason        | reference   | because             |
      | SKU-1 | SITE-1 | MANUAL        | plan-0001:0 | reason-not-slotting |
      | SKU-1 | SITE-1 | REPLENISHMENT | plan-0001:0 | reason-not-slotting |
      | SKU-1 | SITE-1 | CONSOLIDATION |             | reason-not-slotting |
      | SKU-1 | SITE-1 |               | plan-0001:0 | reason-not-slotting |
      | SKU-1 | SITE-1 | slotting      | plan-0001:0 | reason-not-slotting |
      | SKU-1 | SITE-1 | MANUAL        | plan-0099:0 | reason-not-slotting |
      | SKU-1 | SITE-1 | SLOTTING      | plan-0099:0 | unknown-plan        |
      | SKU-1 | SITE-1 | SLOTTING      | plan-0001:2 | unknown-move        |
      | SKU-2 | SITE-1 | SLOTTING      | plan-0001:0 | sku-mismatch        |
      | SKU-1 | SITE-2 | SLOTTING      | plan-0001:0 | site-mismatch       |
      | SKU-1 | SITE-2 | SLOTTING      |             | no-current-plan     |
      | SKU-2 | SITE-1 | SLOTTING      |             | no-matching-move    |

  Scenario: A relocation of a Draft plan's move is not an execution
    Given I generate a slot plan
    When inventory-storage reports relocation "REL-9" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0002:0"
    Then the relocation was observed as "unmatched" because "plan-not-approved"
    And plan number 2 has executed 0 of 0 moves

  Scenario: A relocation of a Rejected plan's move is not an execution
    Given I generate a slot plan
    And I reject plan number 2 with reason "Not now."
    When inventory-storage reports relocation "REL-9" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0002:0"
    Then the relocation was observed as "unmatched" because "plan-not-approved"

  Scenario: A Superseded plan keeps executing the moves that were already released
    Given I generate a slot plan
    And I approve plan number 2
    When inventory-storage reports relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the relocation was observed as "applied"
    And plan number 1 is "Superseded"
    And plan number 1 has executed 1 of 2 moves

  Scenario: Without a reference only the site's current Approved plan is matched, never a Superseded one
    Given I generate a slot plan
    And I approve plan number 2
    When inventory-storage reports relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing ""
    Then the relocation was observed as "unmatched" because "no-matching-move"
    And plan number 1 has executed 0 of 2 moves
    And plan number 2 has executed 0 of 0 moves

  Scenario: Progress is advisory: a newer plan can be approved while the older one is half executed
    Given inventory-storage reports relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    And I generate a slot plan
    When I approve plan number 2
    Then the response status is 200
    And the response field "supersedesPlanId" is plan number 1
    And plan number 1 has executed 1 of 2 moves

  Scenario: Recording a relocation never changes the plan or publishes an event
    Given inventory-storage reports relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    When I GET "/slot-plans/plan-0001"
    Then the response field "state" is "Approved"
    And the response field "version" is "2"
    And the outbox event types are "SlotPlanGenerated, SlotPlanApproved"

  Scenario Outline: A relocation with a missing or unusable field is a poison message, dead-lettered at once
    When the inventory consumer receives the event "evt-bad" of type "StockRelocated" with data <data>
    Then the delivery was dead-lettered as a poison message
    And no relocation was observed
    And plan number 1 has executed 0 of 2 moves

    Examples:
      | data                                                                                                                                                                  |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"SKU-1","from_bin":"R-17","to_bin":"A-01","quantity":0,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"SKU-1","from_bin":"R-17","to_bin":"A-01","quantity":-4,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"","site_code":"SITE-1","sku":"SKU-1","from_bin":"R-17","to_bin":"A-01","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"","sku":"SKU-1","from_bin":"R-17","to_bin":"A-01","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"","from_bin":"R-17","to_bin":"A-01","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"SKU-1","from_bin":"","to_bin":"A-01","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"SKU-1","from_bin":"R-17","to_bin":"  ","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0","occurred_at":"2026-10-09T08:30:00Z"} |
      | {"relocation_id":"REL-1","site_code":"SITE-1","sku":"SKU-1","from_bin":"R-17","to_bin":"A-01","quantity":12,"reason":"SLOTTING","source_ref":"plan-0001:0"} |
      | "not an object"                                                                                                                                                       |

  Scenario: A poison relocation claims nothing, so the corrected event under the same id is applied
    Given inventory-storage reports event "evt-A" with relocation "REL-1" of 0 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    And the delivery was dead-lettered as a poison message
    When inventory-storage reports event "evt-A" with relocation "REL-1" of 12 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the delivery was accepted
    And the relocation was observed as "applied"
    And plan number 1 has executed 1 of 2 moves

  Scenario: Other event types on the inventory topic are ignored
    When the inventory consumer receives the event "evt-adj" of type "StockAdjusted" with data {"sku":"SKU-1","delta":-3}
    Then the delivery was accepted
    And no relocation was observed
    And plan number 1 has executed 0 of 2 moves
