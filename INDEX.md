# Burrow Documentation Index

## Overview

**Burrow** is a Go library for concurrent Kafka consumer processing with ordered commits and at-least-once guarantees.

**Status**: 📋 Design Phase - Ready for Implementation

## Quick Navigation

### For Users

- **[README.md](./README.md)** - Project overview and features
- **[GETTING_STARTED.md](./GETTING_STARTED.md)** - 5-minute quick start guide
- **[docs/API.md](./docs/API.md)** - Complete API reference

### For Implementers

- **[IMPLEMENTATION_CHECKLIST.md](./IMPLEMENTATION_CHECKLIST.md)** - Step-by-step checklist
- **[docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md)** - Detailed implementation guide (Part 1)
- **[docs/IMPLEMENTATION_PLAN_PART2.md](./docs/IMPLEMENTATION_PLAN_PART2.md)** - Implementation guide (Part 2)

### For Understanding Design

- **[docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)** - System architecture and design decisions
- **[docs/EDDIES_COMPARISON.md](./docs/EDDIES_COMPARISON.md)** - Comparison with Eddies library

## Document Descriptions

### README.md
**Purpose**: Project landing page
**Contents**:
- What is Burrow?
- Quick start example
- Features list
- Use cases
- Documentation index

**Read if**: You want a high-level overview

---

### GETTING_STARTED.md
**Purpose**: Tutorial for new users
**Contents**:
- Installation instructions
- 5-minute quick start
- Complete working example
- Key concepts explained
- Common patterns
- Troubleshooting guide

**Read if**: You want to start using Burrow

---

### IMPLEMENTATION_CHECKLIST.md
**Purpose**: Progress tracking for implementers
**Contents**:
- 16 phases of implementation
- Checkbox for each task
- Timeline estimates
- Quality gates
- Critical success factors

**Read if**: You're implementing Burrow

---

### docs/API.md
**Purpose**: Complete API reference
**Contents**:
- All public types documented
- Function signatures
- Parameters and return values
- Usage examples
- Best practices
- Troubleshooting

**Read if**: You need detailed API information

---

### docs/ARCHITECTURE.md
**Purpose**: System design documentation
**Contents**:
- Architecture diagrams
- Component descriptions
- Data flow explanations
- Concurrency model
- Memory management
- Performance characteristics
- Trade-offs

**Read if**: You want to understand how Burrow works internally

---

### docs/IMPLEMENTATION_PLAN.md (Part 1)
**Purpose**: Detailed implementation guide (Phases 1-6)
**Contents**:
- Phase 1: Project Setup
- Phase 2: Core Types
- Phase 3: Offset Tracker (CRITICAL)
- Phase 4: Worker Pool
- Phase 5: Error Tracking
- Phase 6: Commit Manager

**Read if**: You're implementing the core components

---

### docs/IMPLEMENTATION_PLAN_PART2.md (Part 2)
**Purpose**: Implementation guide continued (Phases 7-12)
**Contents**:
- Phase 7: Pool Orchestrator
- Phase 8: Rebalance Handling
- Phase 9: Metrics
- Phase 10: Unit Tests
- Phase 11: Integration Tests
- Phase 12: Examples & Docs

**Read if**: You're implementing the orchestration layer

---

### docs/EDDIES_COMPARISON.md
**Purpose**: Design inspiration and comparison
**Contents**:
- What is Eddies?
- What we adopted from Eddies
- What we added for Kafka
- Architecture comparison
- Code examples
- Why patterns differ

**Read if**: You want to understand design decisions

---

## Reading Order

### For First-Time Users
1. [README.md](./README.md) - Understand what Burrow is
2. [GETTING_STARTED.md](./GETTING_STARTED.md) - Build your first consumer
3. [docs/API.md](./docs/API.md) - Reference when needed

### For Implementers
1. [README.md](./README.md) - Project overview
2. [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) - Understand the design
3. [docs/EDDIES_COMPARISON.md](./docs/EDDIES_COMPARISON.md) - Understand inspirations
4. [IMPLEMENTATION_CHECKLIST.md](./IMPLEMENTATION_CHECKLIST.md) - Track progress
5. [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md) - Implement Part 1
6. [docs/IMPLEMENTATION_PLAN_PART2.md](./docs/IMPLEMENTATION_PLAN_PART2.md) - Implement Part 2

### For Understanding Design Decisions
1. [docs/EDDIES_COMPARISON.md](./docs/EDDIES_COMPARISON.md) - What inspired us
2. [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) - How we adapted it
3. [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md) - Why we chose specific approaches

## Key Concepts (Quick Reference)

### At-Least-Once Guarantees
Messages may be processed multiple times, but never lost.

**Location**: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#at-least-once-guarantees)

### Offset Tracker
Tracks which offsets are processed and computes safe-to-commit offset.

**Location**: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#3-offset-tracker-per-partition)

### Gap Detection
Core algorithm that prevents committing offsets with missing predecessors.

**Location**: [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md#step-31-implement-offsettracker-trackergo)

### Worker Pool
Fixed number of goroutines processing messages concurrently.

**Location**: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#2-worker-pool)

### Commit Manager
Periodically commits offsets to Kafka in correct order.

**Location**: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#4-commit-manager)

## Architecture at a Glance

```
Kafka Consumer → Pool → Worker Pool → Results → Offset Tracker → Commits
                   ↓
            Error Tracker
```

**Details**: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#architecture-diagram)

## API at a Glance

```go
// Create pool
pool, _ := burrow.NewPool(consumer, burrow.DefaultConfig(logger))

// Define processing
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    return doWork(msg)
}

// Run
pool.Run(ctx, processFunc)
```

**Details**: [docs/API.md](./docs/API.md#quick-reference)

## Implementation Status

**Current Status**: 📋 Design Phase Complete

**Next Steps**:
1. Begin Phase 1: Project Setup
2. Follow [IMPLEMENTATION_CHECKLIST.md](./IMPLEMENTATION_CHECKLIST.md)
3. Reference [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md)

**Timeline**: 2-3 weeks for production-ready library

## Contributing

Contributions welcome! See implementation checklist for areas to work on.

**Process**:
1. Pick a phase from checklist
2. Follow implementation plan
3. Write tests
4. Submit PR

## Support

- **Documentation**: This directory
- **Questions**: Ask the team
- **Issues**: TBD (after implementation)

## Version History

- **v1.0.0 (Design)**: 2025-01-10
  - Complete design documentation
  - Ready for implementation

---

**Last Updated**: 2025-01-10

**Maintained by**: vlab-research team
