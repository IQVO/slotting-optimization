# Derived from: docs/contexts/slotting-optimization use-cases.md ("Consumers (Kafka)": ApplyDemandChanged, ApplyProductClassified,
# ApplyPhysicalProfile, ApplyZoneRegistered, ApplyLocationSlotRegistered, ApplyLocationSlotDecommissioned), ubiquitous-language.md
# ("The local copies"), troubleshooting.md ("Plans that come out empty or partial", "Kafka"), eventstorming.md (local-copy flow policies)
@bdd
Feature: The local copies of demand, product and layout facts
  The context never calls a sibling at request time: it keeps event-fed copies.
  Demand is last-writer-wins per order line, product facts follow product-master's
  version, the layout is an upsert in which a decommissioned slot never comes
  back. A CloudEvents id is claimed once; a payload that can never be applied is
  skipped and changes nothing.

  # --- demand -------------------------------------------------------------

  Scenario Outline: The demand window is half-open: a line due at its start counts, one due at its end does not
    Given "SKU-1" has a demand line of 5 units due at "<due>" at site "SITE-1"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the response status is 200
    And the velocity items are "<items>"

    Examples:
      | due                  | items     |
      | 2026-09-10T11:59:59Z |           |
      | 2026-09-10T12:00:00Z | SKU-1:1:5 |
      | 2026-10-08T11:59:59Z | SKU-1:1:5 |
      | 2026-10-08T12:00:00Z |           |

  Scenario Outline: A plan counts the same window as the velocity read
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has a demand line of 5 units due at "<due>" at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then the plan lists <assignments> assignments

    Examples:
      | due                  | assignments |
      | 2026-09-10T11:59:59Z | 0           |
      | 2026-09-10T12:00:00Z | 1           |
      | 2026-10-08T11:59:59Z | 1           |
      | 2026-10-08T12:00:00Z | 0           |

  Scenario Outline: The velocity read takes a window of 1 to 365 days
    Given "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    When I GET "/sku-velocity?windowDays=<days>"
    Then the response status is 200
    And the velocity items are "<items>"

    Examples:
      | days | items     |
      | 3    |           |
      | 4    | SKU-1:3:3 |
      | 365  | SKU-1:3:3 |

  Scenario Outline: A window outside 1 to 365 days is refused with the limit slug
    When I GET "/sku-velocity?windowDays=<days>"
    Then the response status is 400
    And the problem type is "invalid-limit"

    Examples:
      | days |
      | 366  |
      | -5   |
      | abc  |
      | 2.5  |

  Scenario: A removed line comes back when the order line is sent again as active
    Given "SKU-1" has 3 demand lines of 2 units each at site "SITE-1"
    And order "ord-SKU-1-1" line 1 of "SKU-1" at site "SITE-1" is re-sent as "REMOVED" with 2 units
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:2:4"
    When order "ord-SKU-1-1" line 1 of "SKU-1" at site "SITE-1" is re-sent as "ACTIVE" with 2 units
    And I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:3:6"

  Scenario: The last writer wins on the units of a line
    Given "SKU-1" has 2 demand lines of 2 units each at site "SITE-1"
    When order "ord-SKU-1-2" line 1 of "SKU-1" at site "SITE-1" is re-sent as "ACTIVE" with 9 units
    And I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:2:11"

  Scenario: A line re-sent for another site leaves the first site's demand
    Given "SKU-1" has 2 demand lines of 2 units each at site "SITE-1"
    When order "ord-SKU-1-1" line 1 of "SKU-1" at site "SITE-2" is re-sent as "ACTIVE" with 2 units
    And I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:1:2"
    When I GET "/sku-velocity?siteId=SITE-2"
    Then the velocity items are "SKU-1:1:2"

  Scenario: Every line of an order counts as a pick, and the same line number of another order is another line
    Given the demand event "e-1" marks line 1 of order "O-7" as "ACTIVE" for "SKU-1" with 4 units at site "SITE-1"
    And the demand event "e-2" marks line 2 of order "O-7" as "ACTIVE" for "SKU-1" with 6 units at site "SITE-1"
    And the demand event "e-3" marks line 1 of order "O-8" as "ACTIVE" for "SKU-1" with 1 units at site "SITE-1"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:3:11"

  Scenario: A CloudEvents id is claimed once: the redelivery with another payload changes nothing
    Given the demand event "evt-D1" marks line 1 of order "O-7" as "ACTIVE" for "SKU-1" with 5 units at site "SITE-1"
    When the demand event "evt-D1" marks line 1 of order "O-7" as "ACTIVE" for "SKU-1" with 99 units at site "SITE-1"
    Then the last delivery was processed as "duplicate"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:1:5"

  Scenario Outline: A demand event that can never be applied is skipped, claims nothing and changes nothing
    Given "SKU-1" has 1 demand lines of 5 units each at site "SITE-1"
    When the demand consumer receives the event "evt-bad" of type "SiteSkuDemandChanged" with data <data>
    Then the delivery was accepted
    And the delivery was processed with no outcome
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:1:5"

    Examples:
      | data                                                                                                                                                         |
      | {"source_order_id":"ord-SKU-1-1","line_no":0,"site_id":"SITE-1","sku":"SKU-1","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"ACTIVE"}           |
      | {"source_order_id":"ord-SKU-1-1","line_no":-2,"site_id":"SITE-1","sku":"SKU-1","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"REMOVED"}         |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":0,"due_at":"2026-10-05T10:00:00Z","state":"ACTIVE"}           |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":-3,"due_at":"2026-10-05T10:00:00Z","state":"ACTIVE"}          |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"PENDING"}          |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":5,"state":"REMOVED"}                                          |
      | {"source_order_id":"","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"REMOVED"}                    |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE/1","sku":"SKU-1","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"REMOVED"}         |
      | {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"","demanded_units":5,"due_at":"2026-10-05T10:00:00Z","state":"REMOVED"}              |

  Scenario Outline: A removal is applied whatever its units, an activation needs at least one
    Given "SKU-1" has 1 demand lines of 5 units each at site "SITE-1"
    When the demand consumer receives the event "evt-ok" of type "SiteSkuDemandChanged" with data {"source_order_id":"ord-SKU-1-1","line_no":1,"site_id":"SITE-1","sku":"SKU-1","demanded_units":<units>,"due_at":"2026-10-05T10:00:00Z","state":"<state>"}
    Then the last delivery was processed as "applied"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "<items>"

    Examples:
      | state   | units | items     |
      | REMOVED | 0     |           |
      | REMOVED | 5     |           |
      | ACTIVE  | 1     | SKU-1:1:1 |
      | ACTIVE  | 9     | SKU-1:1:9 |

  Scenario Outline: A message the demand consumer cannot act on is skipped without an error
    Given "SKU-1" has 2 demand lines of 5 units each at site "SITE-1"
    When the demand consumer receives <message>
    Then the delivery raised no error
    And I GET "/sku-velocity?siteId=SITE-1"
    And the velocity items are "SKU-1:2:10"

    Examples:
      | message                                          |
      | a message that is not JSON                       |
      | a CloudEvent of a type it does not consume       |
      | a demand event whose data is a string            |

  Scenario: A site other than the one this deployment plans is ignored
    Given the demand consumer only plans site "SITE-1"
    And "SKU-1" has 2 demand lines of 5 units each at site "SITE-1"
    And "SKU-2" has 4 demand lines of 5 units each at site "SITE-2"
    Then the last delivery was processed as "ignored"
    When I GET "/sku-velocity?siteId=SITE-1"
    Then the velocity items are "SKU-1:2:10"
    When I GET "/sku-velocity?siteId=SITE-2"
    Then the velocity items are ""

  # --- product ------------------------------------------------------------

  Scenario Outline: A classification is replaced only by a newer product-master version
    Given the zone "Z-AMB" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-AMB" holds up to 40 kg and 0.5 m3
    And the zone "Z-COLD" of site "SITE-1" with code "FWD" is registered with temperature "Chilled"
    And the slot "C-01" in zone "Z-COLD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And "SKU-1" is classified at version 2 with tags "" and temperature "Chilled"
    And "SKU-1" is classified at version <version> with tags "" and temperature "Ambient"
    And the last delivery was processed as "<outcome>"
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "<slot>" in class "A"

    Examples:
      | version | slot | outcome |
      | 1       | C-01 | stale   |
      | 2       | C-01 | stale   |
      | 3       | A-01 | applied |

  Scenario: Dimensions are guarded by version too: an equal version is stale
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And the product-master measures "SKU-1" at 1000 mm3 and 10 g with version 4
    And the product-master measures "SKU-1" at 900000000 mm3 and 10 g with version 4
    And the last delivery was processed as "stale"
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: Declared dimensions are enough to place a SKU, and a newer measurement replaces them
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And the product-master declares "SKU-1" at 900000000 mm3 and 10 g with version 1
    And I generate a slot plan
    And "SKU-1" is unassigned because "NoCapacityFit"
    And the product-master declares "SKU-1" at 1000 mm3 and 10 g with version 2
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
    Given the product-master measures "SKU-1" at 900000000 mm3 and 10 g with version 3
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoCapacityFit"

  Scenario: A product reported with no effective dimensions loses its size until it is measured again
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And the product-master reports "SKU-1" with no effective dimensions at version 2
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoPhysicalProfile"
    Given the product-master measures "SKU-1" at 1000 mm3 and 10 g with version 3
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: A classification alone gives no size: the SKU still cannot be placed
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" is classified at version 1 with tags "Fragile" and temperature "Ambient"
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoPhysicalProfile"

  Scenario: A CloudEvents id is claimed once for the product copy too
    Given the zone "Z-AMB" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-AMB" holds up to 40 kg and 0.5 m3
    And the zone "Z-COLD" of site "SITE-1" with code "FWD" is registered with temperature "Chilled"
    And the slot "C-01" in zone "Z-COLD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And the product consumer receives the event "evt-P1" of type "ProductClassified" with data {"sku":"SKU-1","handling_tags":[],"temperature_class":"Chilled","version":2}
    When the product consumer receives the event "evt-P1" of type "ProductClassified" with data {"sku":"SKU-1","handling_tags":[],"temperature_class":"Ambient","version":3}
    Then the last delivery was processed as "duplicate"
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "C-01" in class "A"

  Scenario Outline: A product event that can never be applied is skipped and the profile is left as it was
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When the product consumer receives the event "evt-bad" of type "<type>" with data <data>
    Then the delivery was accepted
    And the delivery was processed with no outcome
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

    Examples:
      | type              | data                                                                                              |
      | ProductClassified | {"sku":"SKU-1","handling_tags":["Hazmat"],"temperature_class":"Lukewarm","version":9}             |
      | ProductClassified | {"sku":"SKU-1","handling_tags":["Hazmat"],"temperature_class":"Frozen","version":0}               |
      | ProductClassified | "not an object"                                                                                   |
      | ProductMeasured   | {"sku":"SKU-1","effective":{"volume_mm3":0,"weight_g":10},"effective_source":"measured","version":9} |
      | ProductMeasured   | {"sku":"SKU-1","effective":{"volume_mm3":900000000,"weight_g":0},"effective_source":"measured","version":9} |
      | ProductMeasured   | {"sku":"SKU-1","effective":null,"effective_source":"none","version":0}                            |

  # --- layout -------------------------------------------------------------

  Scenario Outline: A layout event that can never be applied is skipped and the copy is left as it was
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When the layout consumer receives the event "evt-bad" of type "<type>" with data <data>
    Then the delivery was accepted
    And the delivery was processed with no outcome
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

    Examples:
      | type                   | data                                                                                                                  |
      | ZoneRegistered         | {"zoneId":"Z-FWD","siteCode":"SITE-1","areaCode":"A","zoneCode":"FWD","temperatureClass":"Lukewarm","hazmat":true}    |
      | ZoneRegistered         | {"zoneId":"Z-FWD","siteCode":"SITE-1","areaCode":"A","zoneCode":"","temperatureClass":"Ambient","hazmat":true}       |
      | ZoneRegistered         | {"zoneId":"Z-FWD","siteCode":"","areaCode":"A","zoneCode":"FWD","temperatureClass":"Ambient","hazmat":true}          |
      | LocationSlotRegistered | {"locationCode":"A-01","zoneId":"Z-FWD","locationType":"ShelfBin","maxWeightKg":-1,"maxVolumeM3":0}                  |
      | LocationSlotRegistered | {"locationCode":"A-01","zoneId":"Z-FWD","locationType":"ShelfBin","maxWeightKg":0,"maxVolumeM3":-0.5}               |
      | LocationSlotRegistered | {"locationCode":"A-01","zoneId":"","locationType":"ShelfBin","maxWeightKg":40,"maxVolumeM3":0.5}                     |
      | LocationGeometryUpdated | {"locationCode":"A-01","x":4,"y":9}                                                                                  |

  Scenario: A zone registered again replaces the zone: its hazmat flag and temperature follow the latest event
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And I generate a slot plan
    And the plan assigns "SKU-1" to slot "A-01" in class "A"
    When the hazmat zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"
    When the plain zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Chilled"
    And I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"
    When the plain zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: A slot registered again replaces its capacity
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 600000000 mm3 and 10 g
    And I generate a slot plan
    And "SKU-1" is unassigned because "NoCapacityFit"
    When the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 1 m3
    And I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"

  Scenario: A slot registered before its zone is not offered until the zone arrives
    Given "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    And the slot "L-01" in zone "Z-LATE" holds up to 40 kg and 0.5 m3
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"
    Given the zone "Z-LATE" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "L-01" in class "A"

  Scenario: A decommissioned slot does not come back when it is registered again
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And the slot "A-01" is decommissioned
    And the slot "A-01" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"

  Scenario: A slot decommissioned before it was ever registered stays decommissioned
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-09" is decommissioned
    And the slot "A-09" in zone "Z-FWD" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"

  Scenario Outline: Only a slot with the Storage role (an empty role means Storage) is a forward slot
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" with role "<role>" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then <outcome>

    Examples:
      | role      | outcome                                           |
      | Storage   | the plan assigns "SKU-1" to slot "A-01" in class "A" |
      |           | the plan assigns "SKU-1" to slot "A-01" in class "A" |
      | Staging   | "SKU-1" is unassigned because "NoEligibleSlot"    |
      | Reserve   | "SKU-1" is unassigned because "NoEligibleSlot"    |

  Scenario: A zone of another site offers no slot to this site's demand
    Given the zone "Z-S2" of site "SITE-2" with code "FWD" is registered with temperature "Ambient"
    And the slot "S2-01" in zone "Z-S2" holds up to 40 kg and 0.5 m3
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoEligibleSlot"

  Scenario: A slot registered without capacity holds nothing
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the slot "A-01" in zone "Z-FWD" is registered without any capacity
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000 mm3 and 10 g
    When I generate a slot plan
    Then "SKU-1" is unassigned because "NoCapacityFit"

  Scenario: A CloudEvents id is claimed once for the layout copy too
    Given the zone "Z-FWD" of site "SITE-1" with code "FWD" is registered with temperature "Ambient"
    And the layout consumer receives the event "evt-L1" of type "LocationSlotRegistered" with data {"locationCode":"A-01","zoneId":"Z-FWD","locationType":"ShelfBin","maxWeightKg":40,"maxVolumeM3":0.5}
    And the layout consumer receives the event "evt-L1" of type "LocationSlotRegistered" with data {"locationCode":"A-01","zoneId":"Z-FWD","locationType":"ShelfBin","maxWeightKg":1,"maxVolumeM3":0.000001}
    And the last delivery was processed as "duplicate"
    And "SKU-1" has 3 demand lines of 1 units each at site "SITE-1"
    And "SKU-1" has an effective size of 1000000 mm3 and 10 g
    When I generate a slot plan
    Then the plan assigns "SKU-1" to slot "A-01" in class "A"
