# Derived from: docs/contexts/slotting-optimization aggregate-design-canvas.md (state transitions, enforced invariants, corrective policies,
# handled commands), use-cases.md (ApprovePlan, RejectPlan, ListPlans, ListForwardSlots, ListSkuVelocity), troubleshooting.md ("REST problem types",
# NUL guard, body limit), business-context.md (at most one Approved plan per site; Supersede raises no event)
@bdd
Feature: Plan lifecycle, reads and request guards
  A plan's content is written once and never edited; only its decision changes.
  A site has at most one Approved plan: approving supersedes the previous one
  without an event of its own. Reads list plans newest first by cursor, serve the
  Approved plan as the forward-slot map and the demand as SKU velocity.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-02" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 9 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" has 5 demand lines of 1 units each at site "SITE-1"
    And "SKU-2" has an effective size of 1000 mm3 and 10 g

  # --- state machine ----------------------------------------------------------

  Scenario: Superseding raises no event of its own: the newer approval carries it
    Given I generate a slot plan
    And I approve plan number 1
    And I generate a slot plan
    When I approve plan number 2
    Then the response status is 200
    And the outbox event types are "SlotPlanGenerated, SlotPlanApproved, SlotPlanGenerated, SlotPlanApproved"

  Scenario: A site's approvals form a chain and only the newest plan stays Approved
    Given I generate a slot plan
    And I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    And I generate a slot plan
    When I approve plan number 3
    Then the response field "supersedesPlanId" is plan number 2
    And plan number 1 is "Superseded"
    And plan number 2 is "Superseded"
    And plan number 3 is "Approved"
    When I GET "/slot-plans?state=Approved"
    Then the listed plans are numbers "3"
    When I GET "/forward-slots"
    Then the response field "planId" is plan number 3

  Scenario: A plan superseded once is at version 3 and a plan superseded twice is not touched again
    Given I generate a slot plan
    And I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    And I generate a slot plan
    And I approve plan number 3
    When I GET "/slot-plans?siteId=SITE-1"
    Then plan number 1 is listed with 2 assignments, 2 moves and 0 unassigned at version 3
    And plan number 2 is listed with 2 assignments, 0 moves and 0 unassigned at version 3
    And plan number 3 is listed with 2 assignments, 0 moves and 0 unassigned at version 2

  Scenario: A Draft that was generated before a newer plan was approved can still be approved and supersedes it
    Given I generate a slot plan
    And I generate a slot plan
    And I approve plan number 2
    When I approve plan number 1
    Then the response status is 200
    And the response field "supersedesPlanId" is plan number 2
    And plan number 2 is "Superseded"
    And plan number 1 is "Approved"
    When I GET "/forward-slots"
    Then the response field "planId" is plan number 1

  Scenario: Approving a Draft computed earlier does not recompute its content
    Given I generate a slot plan
    And I generate a slot plan
    And I approve plan number 2
    And I approve plan number 1
    When I GET "/slot-plans/plan-0001"
    Then the plan has 2 moves
    And the plan has a "Assign" move of "SKU-1" to "A-01"
    And the plan has a "Assign" move of "SKU-2" to "A-02"
    When I read the "SlotPlanApproved" event of plan number 1
    Then the event field "data.supersedes_plan_id" is plan number 2
    And the event has an "Assign" move of "SKU-1" to "A-01"

  Scenario: A Superseded plan cannot be rejected
    Given I generate a slot plan
    And I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    When I reject plan number 1 with reason "Too late."
    Then the response status is 409
    And the problem type is "plan-not-draft"
    And plan number 1 is "Superseded"

  Scenario: A second rejection leaves the first reason and publishes nothing
    Given I generate a slot plan
    And I reject plan number 1 with reason "Too many relocations."
    When I reject plan number 1 with reason "Changed my mind."
    Then the response status is 409
    And the problem type is "plan-not-draft"
    When I GET "/slot-plans/plan-0001"
    Then the response field "rejectReason" is "Too many relocations."
    And the response field "version" is "2"
    And the outbox event types are "SlotPlanGenerated, SlotPlanRejected"

  Scenario: A reason of only spaces is no reason
    Given I generate a slot plan
    When I reject plan number 1 with reason "     "
    Then the response status is 200
    And the response field "rejectReason" is absent
    When I read the "SlotPlanRejected" event of plan number 1
    Then the event field "data.reason" is absent

  Scenario: A rejected plan is final and never becomes the forward-slot map
    Given I generate a slot plan
    And I reject plan number 1 with reason "No."
    When I GET "/forward-slots"
    Then the response field "planId" is absent
    And the plan lists 0 assignments

  # --- reads --------------------------------------------------------------------

  Scenario: A list item summarises the plan without its content
    Given I generate a slot plan
    When I approve plan number 1
    And I GET "/slot-plans"
    Then plan number 1 is listed with 2 assignments, 2 moves and 0 unassigned at version 2

  Scenario Outline: Plans can be filtered by every state and by site
    Given I generate a slot plan
    And I approve plan number 1
    And I generate a slot plan
    And I approve plan number 2
    And I generate a slot plan
    And I reject plan number 3 with reason "No."
    And I generate a slot plan
    And I generate a slot plan for site "SITE-2"
    When I GET "/slot-plans?<query>"
    Then the response status is 200
    And the listed plans are numbers "<plans>"

    Examples:
      | query                       | plans         |
      | state=Superseded            | 1             |
      | state=Approved              | 2             |
      | state=Rejected              | 3             |
      | state=Draft                 | 5,4           |
      | siteId=SITE-2               | 5             |
      | siteId=SITE-1               | 4,3,2,1       |
      | siteId=SITE-1&state=Draft   | 4             |
      | siteId=SITE-2&state=Approved |              |
      | siteId=SITE-9               |               |

  Scenario Outline: A page limit of 1 to 500 is accepted and anything else is refused
    When I GET "/slot-plans?limit=<limit>"
    Then the response status is <status>

    Examples:
      | limit | status |
      | 1     | 200    |
      | 500   | 200    |
      | 501   | 400    |
      | -1    | 400    |
      | abc   | 400    |

  Scenario: A page that is exactly full has no next page
    Given I generate a slot plan
    And I generate a slot plan
    And I generate a slot plan
    When I GET "/slot-plans?limit=3"
    Then the listed plans are numbers "3,2,1"
    And there is no next page

  Scenario: Walking a list one plan at a time visits every plan once, newest first
    Given I generate a slot plan
    And I generate a slot plan
    And I generate a slot plan
    When I GET "/slot-plans?limit=1"
    Then the listed plans are numbers "3"
    And there is a next page
    When I list the next page
    Then the listed plans are numbers "2"
    And there is a next page
    When I list the next page
    Then the listed plans are numbers "1"
    And there is no next page

  Scenario: The cursor keeps the filter it was handed out with
    Given I generate a slot plan
    And I generate a slot plan
    And I generate a slot plan
    And I generate a slot plan for site "SITE-2"
    When I GET "/slot-plans?siteId=SITE-1&limit=2"
    Then the listed plans are numbers "3,2"
    When I list the next page
    Then the listed plans are numbers "1"
    And there is no next page

  Scenario: A malformed site filter is refused
    When I GET "/slot-plans?siteId=SITE%2F1"
    Then the response status is 400
    And the problem type is "invalid-site-id"

  Scenario: SKU velocity is paged in its own order and the last page has no cursor
    Given "SKU-3" has 1 demand lines of 7 units each at site "SITE-1"
    When I GET "/sku-velocity?limit=2"
    Then the velocity items are "SKU-1:9:9, SKU-2:5:5"
    And there is a next page
    When I list the next page
    Then the velocity items are "SKU-3:1:7"
    And there is no next page

  Scenario: A velocity page that is exactly full has no next page
    When I GET "/sku-velocity?limit=2"
    Then the velocity items are "SKU-1:9:9, SKU-2:5:5"
    And there is no next page

  Scenario Outline: A velocity limit of 1 to 500 is accepted and anything else is refused
    When I GET "/sku-velocity?limit=<limit>"
    Then the response status is <status>

    Examples:
      | limit | status |
      | 1     | 200    |
      | 500   | 200    |
      | 0     | 400    |
      | abc   | 400    |

  Scenario: The velocity window is reported with the demand
    When I GET "/sku-velocity?windowDays=7"
    Then the response field "siteId" is "SITE-1"
    And the response field "windowFrom" is "2026-10-01T12:00:00Z"
    And the response field "windowTo" is "2026-10-08T12:00:00Z"

  Scenario: A site with no Approved plan has an empty forward-slot map under its own name
    When I GET "/forward-slots?siteId=SITE-9"
    Then the response status is 200
    And the response field "siteId" is "SITE-9"
    And the response field "planId" is absent
    And the plan lists 0 assignments

  Scenario: The forward-slot map says when it was approved and keeps the class of each SKU
    Given I generate a slot plan
    And I approve plan number 1
    When I GET "/forward-slots"
    Then the response field "approvedAt" is "2026-10-08T12:00:00Z"
    And the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: A malformed site is refused on the forward-slot map
    When I GET "/forward-slots?siteId=SITE%201"
    Then the response status is 400
    And the problem type is "invalid-site-id"

  Scenario: Liveness answers ok
    When I GET "/healthz"
    Then the response status is 200
    And the response field "status" is "ok"

  # --- request guards -----------------------------------------------------------

  Scenario Outline: A NUL character is a client error and stores nothing
    When I send <verb> "<path>" with the JSON <body>
    Then the response status is <status>
    And the problem type is "<slug>"
    And the outbox event types are ""

    Examples:
      | verb | path        | body                    | status | slug              |
      | POST | /slot-plans | {"siteId":"SITE\u0000"} | 400    | malformed-request |
      | POST | /slot-plans | {"\u0000":1}            | 400    | malformed-request |

  Scenario Outline: A NUL character in the path or query is a client error
    When I GET "<path>"
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | path                        | status | slug               |
      | /slot-plans/plan-%00        | 404    | resource-not-found |
      | /slot-plans?siteId=%00      | 400    | malformed-request  |
      | /forward-slots?siteId=A%00B | 400    | malformed-request  |

  Scenario: A raw NUL byte in the body is a client error and stores nothing
    When I send POST "/slot-plans" with a raw NUL byte inside the siteId
    Then the response status is 400
    And the problem type is "malformed-request"
    And the response field "detail" is "a NUL character (U+0000) is not allowed in a request body"
    And the outbox event types are ""

  Scenario: An escaped backslash before u0000 is not a NUL
    When I send POST "/slot-plans" with the JSON {"siteId":"SITE\\u0000"}
    Then the response status is 201

  Scenario Outline: The request body is limited to 64 KiB
    When I send POST "/slot-plans" with a body of <bytes> bytes
    Then the response status is 400
    And the problem type is "<slug>"

    Examples:
      | bytes | slug              |
      | 65536 | invalid-site-id   |
      | 65537 | malformed-request |
