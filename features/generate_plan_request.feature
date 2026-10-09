# Derived from: apis/openapi.yaml (POST /slot-plans generateSlotPlan, GenerateSlotPlanRequest, 400 BadRequest problems);
# .claude/rules/domain-model.md (Site token rule, half-open Window); docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy.md
@bdd
Feature: Generate a slot plan - request options and validation
  The request is optional. siteId picks the site (default SITE-1) and
  lookbackDays (1 to 365, default 28) sets a demand window that ends now. An
  invalid request is an RFC 7807 problem and stores nothing.

  Background:
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g

  Scenario: An empty request body plans the default site over the default window
    When I POST "/slot-plans"
    Then the response status is 201
    And the response field "siteId" is "SITE-1"
    And the response field "windowFrom" is "2026-09-10T12:00:00Z"
    And the response field "windowTo" is "2026-10-08T12:00:00Z"
    And the response field "generatedAt" is "2026-10-08T12:00:00Z"
    And the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: The window ends now and starts lookbackDays earlier
    When I generate a slot plan looking back 7 days
    Then the response status is 201
    And the response field "windowFrom" is "2026-10-01T12:00:00Z"
    And the response field "windowTo" is "2026-10-08T12:00:00Z"

  Scenario Outline: The lookback decides whether demand due three days ago still counts
    When I generate a slot plan looking back <days> days
    Then the response status is 201
    And the plan lists <assignments> assignments

    Examples:
      | days | assignments |
      | 2    | 0           |
      | 3    | 0           |
      | 4    | 1           |
      | 365  | 1           |

  Scenario: A created plan answers with its location and reads back unchanged
    When I send POST "/slot-plans" with the JSON {}
    Then the response status is 201
    And the response header "Location" is "/slot-plans/plan-0001"
    And the response header "Content-Type" is "application/json"
    And I keep the generated plan
    Then reading plan number 1 returns exactly the plan that was kept

  Scenario: A plan for another site uses only that site's demand and slots
    Given the zone "Z-S2" of site "SITE-2" with code "FWD" is registered with temperature "Ambient"
    And the slot "S2-01" in zone "Z-S2" holds up to 40 kg and 0.5 m3
    And "SKU-9" has 4 demand lines of 1 units each at site "SITE-2"
    And "SKU-9" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan for site "SITE-2"
    Then the response status is 201
    And the response field "siteId" is "SITE-2"
    And the plan assigns "SKU-9" to slot "S2-01" in class "A"
    And "SKU-1" is not in the plan
    And the plan lists 1 assignments

  Scenario Outline: An invalid request is a 400 problem and stores nothing
    When I send POST "/slot-plans" with the JSON <body>
    Then the response status is 400
    And the problem type is "<slug>"
    And the response header "Content-Type" is "application/problem+json"
    And the outbox event types are ""
    When I GET "/slot-plans"
    Then the response list "items" has 0 entries

    Examples:
      | body                                                                  | slug              |
      | {"lookbackDays":0}                                                    | invalid-lookback  |
      | {"lookbackDays":366}                                                  | invalid-lookback  |
      | {"lookbackDays":-1}                                                   | invalid-lookback  |
      | {"siteId":""}                                                         | invalid-site-id   |
      | {"siteId":"SITE/1"}                                                   | invalid-site-id   |
      | {"siteId":"SITE 1"}                                                   | invalid-site-id   |
      | {"siteId":"SSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSS"} | invalid-site-id   |
      | {"lookbackDays":"7"}                                                  | malformed-request |
      | {"lookbackDays":7.5}                                                  | malformed-request |
      | {"horizon":7}                                                         | malformed-request |
      | {"siteId":                                                            | malformed-request |
      | {} {}                                                                 | malformed-request |
