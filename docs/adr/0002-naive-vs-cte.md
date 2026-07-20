# ADR NNNN: Naive vs CTE

Relates to: FR-1.4 (see docs/FRD.md)

## Context

So this decision is regarding how we actually do the BOM expansion or explosion. We have 2 options recurse in go code or use with recursion in postgres and let the db handle.

## Options considered

1. **Option A** — Naive Approach.
   Pros :
   - Generally the simple method.
   Cons :
   - Each call network call to the db increases the network toll
2. **Option B** — CTE With Recursion.
   Pros :
   - It saves the multiple network calls
   Cons:
   - Recursion happens in SQL code so generally more prone to bugs.

## Decision

First we build naive and benchmark it and then we build the cte.

## Why

Even though its complex, its more performant and worth the complexity.

## Consequences

Faster performance.
