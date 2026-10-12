Feature: Read the velocity ledger and list plans

  Scenario: SKU velocity ranks by picks then units
    Given "SKU-A" has 3 demand lines of 10 units each at site "SITE-1"
    And "SKU-B" has 3 demand lines of 20 units each at site "SITE-1"
    And "SKU-C" has 7 demand lines of 1 units each at site "SITE-1"
    When I GET "/sku-velocity?siteId=SITE-1&limit=2"
    Then the response status is 200
    And the velocity items are "SKU-C:7:7, SKU-B:3:60"

  Scenario: A demand line that changes SKU moves its demand with it
    Given "SKU-A" has 3 demand lines of 10 units each at site "SITE-1"
    And the first demand line of "SKU-A" at site "SITE-1" changes to "SKU-B"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-A:2:20, SKU-B:1:10"

  Scenario: Plans are listed newest first with a cursor
    Given I generate a slot plan
    And I generate a slot plan
    And I generate a slot plan
    When I GET "/slot-plans?limit=2"
    Then the response status is 200
    And the listed plans are numbers "3,2"
    And there is a next page
    When I list the next page
    Then the listed plans are numbers "1"
    And there is no next page

  Scenario: Plans can be filtered by state
    Given I generate a slot plan
    And I generate a slot plan
    And I approve plan number 1
    When I GET "/slot-plans?state=Approved"
    Then the listed plans are numbers "1"

  Scenario Outline: Invalid queries are rejected
    When I GET "<path>"
    Then the response status is 400
    And the problem type is "<slug>"

    Examples:
      | path                       | slug           |
      | /slot-plans?limit=0        | invalid-limit  |
      | /slot-plans?state=Done     | invalid-state  |
      | /slot-plans?cursor=!!      | invalid-cursor |
      | /sku-velocity?limit=501    | invalid-limit  |
      | /sku-velocity?windowDays=0 | invalid-limit  |
