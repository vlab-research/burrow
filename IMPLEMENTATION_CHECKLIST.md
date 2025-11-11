# Burrow Implementation Checklist

Use this checklist to track implementation progress. Check off items as you complete them.

## Phase 1: Project Setup ☐

- [ ] Initialize Go module: `go mod init github.com/vlab-research/fly/burrow`
- [ ] Add dependencies:
  - [ ] `github.com/confluentinc/confluent-kafka-go/v2`
  - [ ] `go.uber.org/zap`
  - [ ] `github.com/prometheus/client_golang`
  - [ ] `github.com/stretchr/testify`
- [ ] Create file structure:
  - [ ] `pool.go`
  - [ ] `consumer.go`
  - [ ] `worker.go`
  - [ ] `tracker.go`
  - [ ] `commit.go`
  - [ ] `errors.go`
  - [ ] `types.go`
  - [ ] `config.go`
  - [ ] `metrics.go`
- [ ] Create test directories:
  - [ ] `tests/`
  - [ ] `examples/simple/`

## Phase 2: Core Types ☐

- [ ] Implement `types.go`:
  - [ ] `ProcessFunc` type
  - [ ] `Job` struct
  - [ ] `Result` struct
  - [ ] `Stats` struct
- [ ] Implement `config.go`:
  - [ ] `Config` struct with all fields
  - [ ] `DefaultConfig()` function
  - [ ] `Validate()` method
- [ ] Write tests:
  - [ ] `TestConfig_Validate`
  - [ ] `TestDefaultConfig`

## Phase 3: Offset Tracker ☐ (CRITICAL)

- [ ] Implement `tracker.go`:
  - [ ] `OffsetTracker` struct
  - [ ] `NewOffsetTracker()`
  - [ ] `RecordInflight()`
  - [ ] `MarkProcessed()`
  - [ ] `MarkFailed()`
  - [ ] `GetCommittableOffset()` ← **MOST IMPORTANT**
  - [ ] `GetLastCommitted()`
  - [ ] `CommitOffset()`
  - [ ] `GetInflightCount()`
  - [ ] `WaitForInflight()`
- [ ] Write comprehensive tests:
  - [ ] `TestOffsetTracker_GetCommittableOffset_AllProcessed`
  - [ ] `TestOffsetTracker_GetCommittableOffset_WithGap`
  - [ ] `TestOffsetTracker_GetCommittableOffset_GapAtBeginning`
  - [ ] `TestOffsetTracker_GetCommittableOffset_SingleMessage`
  - [ ] `TestOffsetTracker_GetCommittableOffset_Empty`
  - [ ] `TestOffsetTracker_CommitCleanup`
  - [ ] `TestOffsetTracker_Concurrency` (race detector)

## Phase 4: Worker Pool ☐

- [ ] Implement `worker.go`:
  - [ ] `Worker` struct
  - [ ] `worker.run()` method
  - [ ] `worker.processJob()` method
  - [ ] `WorkerPool` struct
  - [ ] `NewWorkerPool()`
  - [ ] `WorkerPool.Start()`
  - [ ] `WorkerPool.Stop()`
  - [ ] `WorkerPool.SubmitJob()`
  - [ ] `WorkerPool.Results()`
- [ ] Write tests:
  - [ ] `TestWorker_ProcessJob_Success`
  - [ ] `TestWorker_ProcessJob_Failure`
  - [ ] `TestWorkerPool_Start`
  - [ ] `TestWorkerPool_Stop`
  - [ ] `TestWorkerPool_Backpressure`

## Phase 5: Error Tracking ☐

- [ ] Implement `errors.go`:
  - [ ] `ErrorTracker` struct
  - [ ] `NewErrorTracker()`
  - [ ] `RecordError()`
  - [ ] `RecordSuccess()`
  - [ ] `GetStats()`
  - [ ] `isRetriable()` helper
  - [ ] `calculateBackoff()` helper
- [ ] Write tests:
  - [ ] `TestErrorTracker_Threshold`
  - [ ] `TestErrorTracker_Reset`
  - [ ] `TestCalculateBackoff`

## Phase 6: Commit Manager ☐

- [ ] Implement `commit.go`:
  - [ ] `CommitManager` struct
  - [ ] `NewCommitManager()`
  - [ ] `RegisterTracker()`
  - [ ] `UnregisterTracker()`
  - [ ] `RecordMessage()`
  - [ ] `Start()` - commit loop
  - [ ] `tryCommit()` - actual commit logic
- [ ] Write tests:
  - [ ] `TestCommitManager_PeriodicCommit`
  - [ ] `TestCommitManager_BatchCommit`
  - [ ] `TestCommitManager_NoGaps`

## Phase 7: Pool Orchestrator ☐

- [ ] Implement `pool.go`:
  - [ ] `Pool` struct
  - [ ] `NewPool()`
  - [ ] `Run()` - main entry point
  - [ ] `pollLoop()` - Kafka polling
  - [ ] `processResults()` - handle results
  - [ ] `handleFailure()` - retry logic
  - [ ] `getOrCreateTracker()`
  - [ ] `getTracker()`
  - [ ] `GetStats()`
- [ ] Write tests:
  - [ ] `TestPool_NewPool`
  - [ ] `TestPool_Run`
  - [ ] `TestPool_GracefulShutdown`

## Phase 8: Rebalance Handling ☐

- [ ] Add to `pool.go`:
  - [ ] `setupRebalanceCallback()`
  - [ ] `onPartitionsAssigned()`
  - [ ] `onPartitionsRevoked()`
- [ ] Write tests:
  - [ ] `TestPool_PartitionRebalance`

## Phase 9: Metrics ☐

- [ ] Implement `metrics.go`:
  - [ ] Define Prometheus metrics:
    - [ ] `messagesProcessed`
    - [ ] `processingDuration`
    - [ ] `offsetsCommitted`
    - [ ] `offsetLag`
    - [ ] `inflightMessages`
    - [ ] `workerUtilization`
    - [ ] `errorCount`
  - [ ] `recordProcessedMessage()`
  - [ ] `recordCommit()`
  - [ ] `updateInflightGauge()`
- [ ] Add metrics calls throughout codebase
- [ ] Create example with metrics endpoint

## Phase 10: Unit Tests ☐

- [ ] All components have tests
- [ ] Run `go test ./...` - all pass
- [ ] Code coverage > 80%
- [ ] Run `go test -race ./...` - no races

## Phase 11: Integration Tests ☐

- [ ] Setup testcontainers for Kafka
- [ ] Write `TestPool_EndToEnd`:
  - [ ] Produce 100 messages
  - [ ] Process all messages
  - [ ] Verify offsets committed
- [ ] Write `TestPool_WithFailures`:
  - [ ] Some messages fail
  - [ ] Verify retry logic
  - [ ] Verify gap detection
- [ ] Write `TestPool_Rebalance`:
  - [ ] Simulate rebalance
  - [ ] Verify cleanup
  - [ ] Verify no message loss

## Phase 12: Examples ☐

- [ ] Create `examples/simple/main.go`:
  - [ ] Basic usage example
  - [ ] Comments explaining each step
- [ ] Create `examples/with-retry/main.go`:
  - [ ] Custom retry logic
  - [ ] Non-retriable errors
- [ ] Create `examples/with-metrics/main.go`:
  - [ ] Prometheus integration
  - [ ] Grafana dashboard JSON
- [ ] Create `examples/ordered-processing/main.go`:
  - [ ] Ordered processing mode
  - [ ] Trade-offs explained

## Phase 13: Documentation ☐

- [ ] Review all docs for accuracy:
  - [ ] `README.md`
  - [ ] `GETTING_STARTED.md`
  - [ ] `docs/ARCHITECTURE.md`
  - [ ] `docs/API.md`
  - [ ] `docs/IMPLEMENTATION_PLAN.md`
  - [ ] `docs/EDDIES_COMPARISON.md`
- [ ] Add godoc comments:
  - [ ] All public functions
  - [ ] All public types
  - [ ] Package-level doc
- [ ] Create tutorial blog post (optional)

## Phase 14: Performance Testing ☐

- [ ] Write benchmark tests:
  - [ ] `BenchmarkOffsetTracker_GetCommittable`
  - [ ] `BenchmarkPool_Throughput`
  - [ ] `BenchmarkWorkerPool_Processing`
- [ ] Run CPU profiling:
  - [ ] `go test -cpuprofile cpu.prof -bench .`
  - [ ] `go tool pprof cpu.prof`
  - [ ] Optimize hot paths
- [ ] Run memory profiling:
  - [ ] `go test -memprofile mem.prof -bench .`
  - [ ] `go tool pprof mem.prof`
  - [ ] Check for leaks
- [ ] Load testing:
  - [ ] 1M messages test
  - [ ] Measure throughput
  - [ ] Measure latency (p50, p95, p99)

## Phase 15: Production Hardening ☐

- [ ] Security audit:
  - [ ] No SQL injection vectors
  - [ ] No command injection vectors
  - [ ] Secrets handled properly
- [ ] Error handling audit:
  - [ ] No unwrap() or panic() in production code
  - [ ] All errors logged
  - [ ] All errors returned or handled
- [ ] Cleanup audit:
  - [ ] No goroutine leaks
  - [ ] No channel leaks
  - [ ] Resources properly closed
- [ ] Logging audit:
  - [ ] Appropriate log levels
  - [ ] Structured logging
  - [ ] No sensitive data logged
- [ ] Configuration validation:
  - [ ] All configs validated
  - [ ] Good defaults provided
  - [ ] Clear error messages

## Phase 16: Release Preparation ☐

- [ ] Code quality:
  - [ ] Run `golangci-lint run`
  - [ ] Fix all warnings
  - [ ] Format code: `go fmt ./...`
- [ ] Version tagging:
  - [ ] Update version in comments
  - [ ] Create CHANGELOG.md
  - [ ] Tag release: `git tag v1.0.0`
- [ ] Documentation:
  - [ ] Update all dates in docs
  - [ ] Verify all links work
  - [ ] Add contributing guidelines
  - [ ] Add license file (MIT)
- [ ] Publishing:
  - [ ] Push to GitHub
  - [ ] Verify pkg.go.dev shows docs
  - [ ] Announce to team

## Quality Gates

Before considering a phase complete, ensure:

### For Core Components (Phases 3-7)
- ✅ All functions implemented
- ✅ All tests written and passing
- ✅ Code coverage > 80%
- ✅ No race conditions
- ✅ Godoc comments added
- ✅ Manual testing done

### For Documentation (Phase 13)
- ✅ All sections complete
- ✅ Code examples work
- ✅ No broken links
- ✅ Reviewed for clarity

### For Release (Phase 16)
- ✅ All phases 1-15 complete
- ✅ All tests passing
- ✅ Performance acceptable
- ✅ Documentation reviewed
- ✅ Examples verified

## Timeline Estimates

| Phase | Duration | Dependencies |
|-------|----------|--------------|
| 1. Setup | 0.5 day | None |
| 2. Types | 0.5 day | Phase 1 |
| 3. Tracker | 1 day | Phase 2 |
| 4. Workers | 1 day | Phase 2 |
| 5. Errors | 0.5 day | Phase 2 |
| 6. Commits | 1 day | Phases 3, 5 |
| 7. Pool | 2 days | Phases 3-6 |
| 8. Rebalance | 0.5 day | Phase 7 |
| 9. Metrics | 1 day | Phase 7 |
| 10. Unit Tests | 1 day | Phases 2-9 |
| 11. Integration | 2 days | Phase 10 |
| 12. Examples | 1 day | Phase 11 |
| 13. Docs | 1 day | Phase 12 |
| 14. Performance | 1 day | Phase 11 |
| 15. Hardening | 1 day | Phase 14 |
| 16. Release | 0.5 day | Phase 15 |

**Total: ~16 days** (2-3 weeks)

## Critical Success Factors

1. **Phase 3 (Offset Tracker) is CRITICAL**
   - Gap detection algorithm must be correct
   - Spend extra time testing this
   - This is the heart of at-least-once guarantees

2. **Phase 11 (Integration Tests) validates everything**
   - Don't skip this
   - Catches issues unit tests miss

3. **Phase 14 (Performance) may reveal issues**
   - May need to refactor for performance
   - Budget time for optimization

## Getting Help

If stuck:
1. Review `docs/ARCHITECTURE.md` for design
2. Check `docs/IMPLEMENTATION_PLAN.md` for detailed steps
3. Look at `docs/EDDIES_COMPARISON.md` for inspiration
4. Study the Confluent Parallel Consumer (Java) source code
5. Ask the team!

## Progress Tracking

**Current Phase**: _______________

**Completion**: ___ / 16 phases complete

**Blockers**: ______________________

**Next Steps**: ____________________

---

**Good luck building Burrow!** 🚀

Remember: The goal is a production-ready library that teams can depend on.
Take the time to do it right!
