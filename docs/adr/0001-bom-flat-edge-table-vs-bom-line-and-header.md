# ADR NNNN: Flat Table vs Multi Table approach.

Status: proposed | accepted | superseded by ADR-NNNN
Date: 2026-07-20
Relates to: FR-1.2 (see docs/FRD.md)

## Context

Okay so while i was learning i thought to design the bom sql table for storing recipe.
One approach i always follow. I start with a simple design this philosophy of not starting with a complex one is directly from elon musk who always wants to simplify things and add complexity only when needed.
- Also i was not aware of multiple boms.

## Options considered

1. **Option A** — A single flat bom edge table with the following fiels
- parentId, ComponentId, Qty.
   Pros / cons.
   - pros
      - its simple to come up with and simple to code.
   - Cons
      - It does not mimic real life production scenarios where multiple recipe exists.
   - Lesson
      - Always try to ask what is the inherent assumption that i am making with design choice that might not be true.
      - it is okay if your initial design does not fulfill all requirements of a complex physical world but always know that your inital design will be simple and you will ask what complexity does it not fulfill and then work on that.
2. **Option B** - Use a Bom Header and Bom Lines approach.
   - pros
      - With this appraoch you can have multiple boms or versioning for a single item.
      - its a design that would satisfy multiple boms
   - cons
      - A flat table lets you query WHERE parent_item_id = ? directly
      


## Decision

Since in real world we have multiple boms.
We chose the approach to have bom lines and bom headers.
## Why

the trade off is its more complex than a single edge table and it needs joins to connect the 2 tables. but this trade off is justified because this will result in the correct output or function. it is not a uneccessary trade off.

## Consequences

-
