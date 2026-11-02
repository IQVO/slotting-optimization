---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 19
---

# Sequence diagrams

The main runtime interactions inside `cmd/api`, drawn from the code with
their error branches. They show the only runtime, Postgres and Kafka (ADR 0011),
with the idempotency middleware always on.

## Generate a plan

```mermaid
sequenceDiagram
    actor C as Client
    participant R as chi router
    participant I as Idempotency middleware
    participant G as GeneratePlan
    participant DB as Postgres
    participant P as Planner
    C->>R: POST /slot-plans, Idempotency-Key k, body
    R->>I: route
    alt no key
        I-->>C: 400 idempotency-key-required
    end
    I->>DB: BEGIN, INSERT idempotency_keys ON CONFLICT DO NOTHING
    alt key already committed
        I->>DB: SELECT stored outcome
        alt same body hash
            I-->>C: stored status, headers and body (replay, Idempotency-Replayed: true)
        else different method, path or body
            I-->>C: 409 idempotency-key-reused
        end
    end
    I->>G: Handle(siteId, lookbackDays) inside the bound transaction
    G->>G: resolve site and lookback (400 invalid-site-id or invalid-lookback)
    G->>DB: Velocity, Profiles.Many, ForwardSlots, Current Approved plan
    G->>P: Plan(input)
    P-->>G: assignments, moves, unassigned
    G->>G: slotplan.Generate (Draft, version 1, SlotPlanGenerated)
    G->>DB: INSERT slot_plans and children, INSERT outbox_events
    G-->>I: plan
    I->>DB: UPDATE idempotency_keys with 201 response, COMMIT
    I-->>C: 201 Created, Location /slot-plans/planId
```

Source: `internal/adapters/inbound/http/idempotency.go`,
`internal/adapters/inbound/http/handlers.go`,
`internal/application/usecases/generate_plan.go`.

A 5xx from the handler rolls the whole transaction back and is not cached,
so the same key can retry.

## Approve a plan

```mermaid
sequenceDiagram
    actor C as Client
    participant A as ApprovePlan
    participant U as UnitOfWork
    participant DB as Postgres
    C->>A: POST /slot-plans/planId/approvals
    A->>A: NewPlanID (400 invalid-plan-id)
    A->>U: Do
    U->>DB: BEGIN
    A->>DB: Get plan (404 plan-not-found)
    alt state is not Draft
        A-->>C: 409 plan-not-draft
    end
    A->>DB: Current Approved plan of the site
    opt a previous Approved plan exists
        A->>A: previous.Supersede
        A->>DB: UPDATE previous WHERE version = loaded
        alt version changed
            A-->>C: 409 approved-plan-conflict
        end
    end
    A->>A: plan.Approve(previousId) raises SlotPlanApproved
    A->>DB: UPDATE plan WHERE version = loaded, INSERT outbox_events
    alt unique index uq_slot_plans_one_approved_per_site violated
        A-->>C: 409 approved-plan-conflict
    end
    U->>DB: COMMIT
    A-->>C: 200 Approved plan
```

Source: `internal/application/usecases/decide_plan.go`,
`internal/adapters/outbound/postgres/slot_plan_repository.go`.

Reject follows the same shape without the supersede step: `Get`,
`plan.Reject(reason)` (`409 plan-not-draft`, `400 reject-reason-too-long`),
`UPDATE ... WHERE version = loaded`, outbox insert, commit.

## Consume one message

```mermaid
sequenceDiagram
    participant K as Kafka topic
    participant L as consumeLoop
    participant H as Consumer.HandleMessage
    participant UC as Apply use case
    participant DB as Postgres
    participant DLQ as topic.dlq
    K->>L: FetchMessage
    L->>H: value
    H->>H: cloudevents.Decode
    alt not a CloudEvent, unknown type, bad payload, invalid values
        H-->>L: nil (WARN logged, or silently for an unknown type)
    else known type
        H->>UC: Handle(eventId, change)
        UC->>DB: BEGIN, claim processed_events (consumer, eventId)
        alt already claimed
            UC-->>H: duplicate
        else claimed
            UC->>DB: upsert the copy (version guard where relevant)
            UC->>DB: COMMIT
            UC-->>H: applied, stale or ignored
        end
    end
    alt transient error
        L->>L: retry with backoff 200 ms doubling to 5 s
        L->>DLQ: after 5 attempts publish with x-dlq headers
    end
    L->>K: CommitMessages (retried until it succeeds)
```

Source: `internal/adapters/inbound/kafka/{kafka,consumer,deadletter}.go`,
`internal/application/usecases/consumers.go`.

## Record a relocation (execution progress)

The StockRelocated consumer (ADR 0010) uses the same loop, with two
differences: a `StockRelocated` that can never be applied is dead-lettered at
once instead of skipped, and a relocation that matches no move is committed
as `unmatched`.

```mermaid
sequenceDiagram
    participant K as warehouse.inventory.events
    participant L as consumeLoop
    participant UC as RecordRelocation
    participant DB as Postgres
    participant DLQ as warehouse.inventory.events.dlq
    K->>L: FetchMessage (StockRelocated)
    alt data malformed or a required field missing
        L->>DLQ: publish at once with x-dlq headers
    else valid
        L->>UC: Handle(eventId, relocation)
        UC->>DB: BEGIN, claim processed_events (slotting-relocation, eventId)
        alt source_ref is plan_id:move_seq
            UC->>DB: load that plan, check site, position and SKU
        else no move reference
            UC->>DB: load the site's Approved plan, match SKU and bins
        end
        alt matched
            UC->>DB: INSERT slot_plan_move_executions ON CONFLICT (relocation_id) DO NOTHING
        else unmatched
            UC->>UC: reason (unknown-plan, sku-mismatch, ...)
        end
        UC->>DB: COMMIT
        UC-->>L: applied, duplicate or unmatched (counted)
    end
    L->>K: CommitMessages
```

A cancelled context (shutdown) returns before any dead-letter call: the
offset stays uncommitted and the message is redelivered.

Source: `internal/adapters/inbound/kafka/stock_consumer.go`,
`internal/application/usecases/record_relocation.go`,
`internal/domain/slotplan/execution.go`.

## Relay the outbox

```mermaid
sequenceDiagram
    participant R as Relay.Run
    participant DB as Postgres
    participant S as RelaySink
    participant K as warehouse.slotting-optimization.events
    loop every OUTBOX_RELAY_INTERVAL, or at once after a full batch
        R->>DB: BEGIN, SELECT up to 100 unpublished rows with attempts below OUTBOX_MAX_ATTEMPTS, FOR UPDATE SKIP LOCKED
        loop each row in id order
            R->>S: Send(message)
            S->>K: WriteMessages, key plan id, RequireAll acks
            alt send failed
                R->>DB: UPDATE attempts and last_error RETURNING attempts, COMMIT
                opt attempts reached OUTBOX_MAX_ATTEMPTS (ADR 0008)
                    R->>R: log ERROR row reached the attempt cap (row excluded from later claims)
                end
                R->>R: log outbox relay pass failed, wait the interval
            else sent
                R->>DB: UPDATE published_at
            end
        end
        R->>DB: COMMIT
    end
```

Source: `internal/adapters/outbound/outbox/relay.go`,
`internal/adapters/outbound/postgres/outbox_repository.go`,
`internal/adapters/outbound/kafka/relay_sink.go`.
