Feature: Approve or reject a slot plan
  Only a Draft can be decided. Approving a plan makes it the site's one current
  plan and supersedes the one before it; the forward-slot map is the Approved
  plan's assignments.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan

  Scenario: Approving a Draft
    When I approve plan number 1
    Then the response status is 200
    And the response field "state" is "Approved"
    And the response field "version" is "2"
    And the response field "approvedAt" is "2026-10-08T12:00:00Z"
    And the response field "supersedesPlanId" is absent
    And the outbox event types are "SlotPlanGenerated, SlotPlanApproved"

  Scenario: Approving again is a conflict
    Given I approve plan number 1
    When I approve plan number 1
    Then the response status is 409
    And the problem type is "plan-not-draft"
    And the outbox event types are "SlotPlanGenerated, SlotPlanApproved"

  Scenario: Approving a newer plan supersedes the previous one
    Given I approve plan number 1
    And I generate a slot plan
    When I approve plan number 2
    Then the response status is 200
    And the response field "supersedesPlanId" is plan number 1
    And plan number 1 is "Superseded"
    And plan number 2 is "Approved"

  Scenario: The forward-slot map follows the approved plan
    Given I approve plan number 1
    When I GET "/forward-slots"
    Then the response status is 200
    And the response field "siteId" is "SITE-1"
    And the response field "planId" is plan number 1
    And the plan assigns "SKU-1" to slot "A-01" in class "A"
    And the plan assigns "SKU-2" to slot "A-02" in class "A"

  Scenario: A site without an approved plan has an empty map
    When I GET "/forward-slots"
    Then the response status is 200
    And the response field "planId" is absent
    And the plan lists 0 assignments

  Scenario: The next plan keeps every SKU in its slot when nothing changed
    Given I approve plan number 1
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    And the plan assigns "SKU-2" to slot "A-02" in class "A"
    And the plan has 0 moves

  Scenario: A busier newcomer takes the best slot and a SKU that no longer fits is vacated
    Given I approve plan number 1
    And "SKU-3" has 20 demand lines of 1 units each at site "SITE-1"
    And "SKU-3" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" gets a newer physical profile of 900000000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-3" to slot "A-01" in class "A"
    And the plan assigns "SKU-1" to slot "A-02" in class "A"
    And the plan has a "Relocate" move of "SKU-1" from "A-01" to "A-02"
    And the plan has a "Assign" move of "SKU-3" to "A-01"
    And the plan has a "Vacate" move of "SKU-2" from "A-02"
    And "SKU-2" is unassigned because "NoCapacityFit"

  Scenario: Rejecting a Draft keeps the reason
    When I reject plan number 1 with reason "Too many relocations."
    Then the response status is 200
    And the response field "state" is "Rejected"
    And the response field "rejectReason" is "Too many relocations."
    And the outbox event types are "SlotPlanGenerated, SlotPlanRejected"

  Scenario: A rejected plan can no longer be approved or rejected
    Given I reject plan number 1 with reason "No."
    When I approve plan number 1
    Then the response status is 409
    And the problem type is "plan-not-draft"
    When I reject plan number 1 with reason "Again."
    Then the response status is 409

  Scenario Outline: Unknown or invalid plans
    When I <verb> "<path>"
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | verb | path                             | status | slug            |
      | GET  | /slot-plans/plan-unknown         | 404    | plan-not-found  |
      | GET  | /slot-plans/nope                 | 400    | invalid-plan-id |
      | POST | /slot-plans/plan-unknown/approve | 404    | plan-not-found  |
