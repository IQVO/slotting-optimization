# Derived from: repo docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work.md ("Matching": the bins each move kind fixes),
# docs/contexts/slotting-optimization use-cases.md (RecordRelocation), troubleshooting.md (no-matching-move)
@bdd
Feature: Matching a relocation without a move reference to the bins of each move kind
  When the relocation carries no move reference, the site's current Approved
  plan is searched for the move of that SKU whose bins agree with its kind:
  Relocate needs both slots, Assign only the slot it fills (from any bin),
  Vacate only the slot it empties (to any bin).

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
    And "SKU-3" has 20 demand lines of 1 units each at site "SITE-1"
    And "SKU-3" has an effective size of 1000 mm3 and 10 g
    And "SKU-2" gets a newer physical profile of 900000000 mm3 and 10 g
    And I generate a slot plan
    And I approve plan number 2

  Scenario: The three move kinds are numbered by SKU
    When I GET "/slot-plans/plan-0002"
    Then plan number 2 has executed 0 of 3 moves
    And move 0 of the execution is a "Relocate" of "SKU-1" not executed
    And move 1 of the execution is a "Vacate" of "SKU-2" not executed
    And move 2 of the execution is a "Assign" of "SKU-3" not executed

  Scenario Outline: A relocation whose bins agree with the move's kind executes that move
    When inventory-storage reports relocation "REL-1" of 4 units of "<sku>" from bin "<from>" to bin "<to>" in site "SITE-1" for reason "SLOTTING" referencing ""
    Then the relocation was observed as "applied"
    And plan number 2 has executed 1 of 3 moves
    And move <seq> of the execution is a "<kind>" of "<sku>" executed with 4 units

    Examples:
      | sku   | from | to   | seq | kind     |
      | SKU-1 | A-01 | A-02 | 0   | Relocate |
      | SKU-2 | A-02 | R-30 | 1   | Vacate   |
      | SKU-2 | A-02 | R-41 | 1   | Vacate   |
      | SKU-3 | R-17 | A-01 | 2   | Assign   |
      | SKU-3 | R-52 | A-01 | 2   | Assign   |

  Scenario Outline: A relocation whose bins disagree with the move's kind matches nothing
    When inventory-storage reports relocation "REL-1" of 4 units of "<sku>" from bin "<from>" to bin "<to>" in site "SITE-1" for reason "SLOTTING" referencing ""
    Then the delivery was accepted
    And the relocation was observed as "unmatched" because "no-matching-move"
    And plan number 2 has executed 0 of 3 moves

    Examples:
      | sku   | from | to   |
      | SKU-1 | A-01 | A-03 |
      | SKU-1 | R-17 | A-02 |
      | SKU-2 | A-01 | R-30 |
      | SKU-3 | R-17 | A-02 |
      | SKU-9 | R-17 | A-01 |

  Scenario: A move reference decides, the bins are not consulted
    When inventory-storage reports relocation "REL-1" of 4 units of "SKU-3" from bin "R-99" to bin "R-98" in site "SITE-1" for reason "SLOTTING" referencing "plan-0002:2"
    Then the relocation was observed as "applied"
    And plan number 2 has executed 1 of 3 moves
    And move 2 of the execution is a "Assign" of "SKU-3" executed with 4 units

  Scenario: The move of the superseded plan is still reachable by its reference
    When inventory-storage reports relocation "REL-1" of 9 units of "SKU-1" from bin "R-17" to bin "A-01" in site "SITE-1" for reason "SLOTTING" referencing "plan-0001:0"
    Then the relocation was observed as "applied"
    And plan number 1 is "Superseded"
    And plan number 1 has executed 1 of 2 moves
    And plan number 2 has executed 0 of 3 moves
