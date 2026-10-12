# Derived from: apis/openapi.yaml (approveSlotPlan, rejectSlotPlan, getSlotPlan, PlanConflict/PlanNotFound/BadRequest problems);
# apis/asyncapi.yaml (SlotPlanGenerated, SlotPlanApproved, SlotPlanRejected, CloudEvents envelope);
# .claude/rules/domain-model.md (SlotPlan lifecycle, one Approved plan per site); docs/adr/0002, docs/adr/0004
@bdd
Feature: Decide on a slot plan - lifecycle edges and the events it publishes
  Only a Draft can be decided; the decision is the one transaction that
  changes the site's forward-slot map and it publishes CloudEvents 1.0 events
  through the outbox. A refused request changes nothing and publishes nothing.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan

  Scenario: Rejecting without a reason leaves the reason out
    When I reject plan number 1 without a reason
    Then the response status is 200
    And the response field "state" is "Rejected"
    And the response field "rejectedAt" is "2026-10-08T12:00:00Z"
    And the response field "version" is "2"
    And the response field "rejectReason" is absent
    When I read the "SlotPlanRejected" event of plan number 1
    Then the event field "data.rejected_at" is "2026-10-08T12:00:00Z"
    And the event field "data.reason" is absent

  Scenario: The reject reason is trimmed
    When I reject plan number 1 with reason "   Too risky before peak.  "
    Then the response status is 200
    And the response field "rejectReason" is "Too risky before peak."

  Scenario Outline: The reject reason may have at most 500 characters
    When I reject plan number 1 with a reason made of <count> times "<unit>"
    Then the response status is <status>
    And plan number 1 is "<state>"
    And the outbox event types are "<events>"

    Examples:
      | count | unit | status | state    | events                              |
      | 500   | x    | 200    | Rejected | SlotPlanGenerated, SlotPlanRejected |
      | 501   | x    | 400    | Draft    | SlotPlanGenerated                   |
      | 500   | é    | 200    | Rejected | SlotPlanGenerated, SlotPlanRejected |
      | 501   | é    | 400    | Draft    | SlotPlanGenerated                   |

  Scenario: A reason that is too long is a problem of its own
    When I reject plan number 1 with a reason made of 501 times "x"
    Then the response status is 400
    And the problem type is "reject-reason-too-long"
    And the response field "title" is "Reject reason is too long"

  Scenario: An unknown field in the reject body is refused
    When I send POST "/slot-plans/plan-0001/reject" with the JSON {"why":"no"}
    Then the response status is 400
    And the problem type is "malformed-request"
    And plan number 1 is "Draft"

  Scenario Outline: Plan ids that are unknown or malformed
    When I <verb> "<path>"
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | verb | path                                                                | status | slug            |
      | POST | /slot-plans/plan-unknown/reject                                     | 404    | plan-not-found  |
      | POST | /slot-plans/nope/approve                                            | 400    | invalid-plan-id |
      | POST | /slot-plans/nope/reject                                             | 400    | invalid-plan-id |
      | POST | /slot-plans/plan-/approve                                           | 400    | invalid-plan-id |
      | GET  | /slot-plans/plan-a%2Fb                                              | 400    | invalid-plan-id |
      | GET  | /slot-plans/plan-a%20b                                              | 400    | invalid-plan-id |
      | GET  | /slot-plans/plan-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx | 400    | invalid-plan-id |
      | GET  | /slot-plans/plan-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx  | 404    | plan-not-found  |

  Scenario: Every error is an RFC 7807 problem document
    When I send GET "/slot-plans/plan-unknown"
    Then the response status is 404
    And the response header "Content-Type" is "application/problem+json"
    And the response field "type" is "https://errors.slotting-optimization.warehouse-systems.dev/plan-not-found"
    And the response field "title" is "Slot plan not found"
    And the response field "status" is "404"
    And the response field "instance" is "/slot-plans/plan-unknown"
    And the response field "detail" is present

  Scenario: An approved plan cannot be rejected
    Given I approve plan number 1
    When I reject plan number 1 with reason "Changed my mind."
    Then the response status is 409
    And the problem type is "plan-not-draft"
    And plan number 1 is "Approved"
    And the outbox event types are "SlotPlanGenerated, SlotPlanApproved"

  Scenario: A superseded plan cannot be approved again
    Given I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    When I approve plan number 1
    Then the response status is 409
    And the problem type is "plan-not-draft"
    And plan number 1 is "Superseded"
    And plan number 2 is "Approved"

  Scenario: Superseding stamps the old plan and bumps its version once
    Given I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    When I GET "/slot-plans/plan-0001"
    Then the response field "state" is "Superseded"
    And the response field "version" is "3"
    And the response field "approvedAt" is "2026-10-08T12:00:00Z"
    And the response field "supersededAt" is "2026-10-08T12:00:00Z"
    When I GET "/slot-plans/plan-0002"
    Then the response field "version" is "2"
    And the response field "supersededAt" is absent

  Scenario: Rejecting a Draft leaves the approved plan and the forward-slot map alone
    Given I approve plan number 1
    And I generate a slot plan
    When I reject plan number 2 with reason "Not now."
    Then the response status is 200
    And plan number 1 is "Approved"
    When I GET "/forward-slots"
    Then the response field "planId" is plan number 1
    And the plan lists 2 assignments

  Scenario: A new Draft does not change the forward-slot map until it is approved
    Given I approve plan number 1
    And I generate a slot plan
    When I GET "/forward-slots"
    Then the response field "planId" is plan number 1

  Scenario: Moves are computed against the approved plan, never against a Draft
    When I generate a slot plan
    Then the plan has 2 moves
    And the plan has a "Assign" move of "SKU-1" to "A-01"
    And the plan has a "Assign" move of "SKU-2" to "A-02"

  Scenario: Each site keeps its own approved plan
    Given I generate a slot plan for site "SITE-2"
    And I approve plan number 1
    When I approve plan number 2
    Then the response status is 200
    And the response field "supersedesPlanId" is absent
    And plan number 1 is "Approved"
    And plan number 2 is "Approved"
    When I GET "/forward-slots?siteId=SITE-2"
    Then the response field "siteId" is "SITE-2"
    And the response field "planId" is plan number 2
    When I GET "/forward-slots"
    Then the response field "planId" is plan number 1

  Scenario: Generated, approved and rejected events are CloudEvents 1.0 envelopes
    Given I approve plan number 1
    And I generate a slot plan
    And I reject plan number 2 with reason "No."
    When I read the "SlotPlanApproved" event of plan number 1
    Then the event field "specversion" is "1.0"
    And the event field "id" is present
    And the event field "source" is "/warehouse/slotting-optimization"
    And the event field "type" is "com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved"
    And the event field "subject" is plan number 1
    And the event field "time" is "2026-10-08T12:00:00Z"
    And the event field "datacontenttype" is "application/json"
    And the event field "dataschema" is "urn:warehouse:slotting-optimization:events:SlotPlanApproved:v1"
    And the event Kafka key is plan number 1
    And the event carries the CloudEvents content type header
    When I read the "SlotPlanRejected" event of plan number 2
    Then the event field "type" is "com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected"
    And the event field "dataschema" is "urn:warehouse:slotting-optimization:events:SlotPlanRejected:v1"
    And the event Kafka key is plan number 2
    And the outbox event ids are all distinct

  Scenario: SlotPlanGenerated reports the size of the proposal, not its content
    When I read the "SlotPlanGenerated" event of plan number 1
    Then the event field "data.plan_id" is plan number 1
    And the event field "data.site_id" is "SITE-1"
    And the event field "data.policy" is "abc-velocity-v1"
    And the event field "data.window_from" is "2026-09-10T12:00:00Z"
    And the event field "data.window_to" is "2026-10-08T12:00:00Z"
    And the event field "data.assignment_count" is "2"
    And the event field "data.move_count" is "2"
    And the event field "data.unassigned_count" is "0"
    And the event field "data.assignments" is absent

  Scenario: SlotPlanApproved carries the full map and the moves
    When I approve plan number 1
    And I read the "SlotPlanApproved" event of plan number 1
    Then the event field "data.plan_id" is plan number 1
    And the event field "data.site_id" is "SITE-1"
    And the event field "data.approved_at" is "2026-10-08T12:00:00Z"
    And the event field "data.supersedes_plan_id" is absent
    And the event field "data.assignments" has 2 items
    And the event assigns "SKU-1" to slot "A-01"
    And the event assigns "SKU-2" to slot "A-02"
    And the event field "data.moves" has 2 items
    And the event has an "Assign" move of "SKU-1" to "A-01"
    And the event has an "Assign" move of "SKU-2" to "A-02"

  Scenario: SlotPlanApproved names the superseded plan and every kind of move
    Given I approve plan number 1
    And "SKU-3" has 20 demand lines of 1 units each at site "SITE-1"
    And "SKU-3" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" gets a newer physical profile of 900000000 mm3 and 10 g
    And I generate a slot plan
    When I approve plan number 2
    And I read the "SlotPlanApproved" event of plan number 2
    Then the event field "data.supersedes_plan_id" is plan number 1
    And the event field "data.assignments" has 2 items
    And the event assigns "SKU-3" to slot "A-01"
    And the event assigns "SKU-1" to slot "A-02"
    And the event has an "Assign" move of "SKU-3" to "A-01"
    And the event has a "Relocate" move of "SKU-1" from "A-01" to "A-02"
    And the event has a "Vacate" move of "SKU-2" from "A-02"

  Scenario: SlotPlanRejected carries the reason
    When I reject plan number 1 with reason "Too many relocations."
    And I read the "SlotPlanRejected" event of plan number 1
    Then the event field "data.plan_id" is plan number 1
    And the event field "data.site_id" is "SITE-1"
    And the event field "data.reason" is "Too many relocations."

  Scenario: Nothing is published when a decision is refused
    Given I reject plan number 1 with reason "First."
    When I reject plan number 1 with reason "Second."
    And I approve plan number 1
    And I approve plan number 9
    Then the outbox event types are "SlotPlanGenerated, SlotPlanRejected"
