## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 397 | 0 | 0 | 3 | 351s |
| macOS race | 399 | 0 | 0 | 1 | 289s |
| Linux coverage | 412 | 0 | 0 | 3 | 153s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 52406 | 55280 ±9% | ~ | 0.394 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11279 | 11279 ±0% | ~ | 0.818 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 40.9 | 41.6 ±9% | +1.6% | 0.026 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 63189 | 64787 ±12% | ~ | 0.485 | 600000 |
| internal/engine.AddAndGrant | B/op | 19167 | 19215 ±1% | ~ | 0.485 |  |
| internal/engine.AddAndGrant | allocs/op | 190 | 190 ±0% | ~ | 0.394 | 240 |
| internal/engine.GrantCycle | ns/op | 56690 | 56415 ±9% | ~ | 0.937 | 500000 |
| internal/engine.GrantCycle | B/op | 16468 | 16628 ±1% | ~ | 0.065 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 176 ±1% | ~ | 0.065 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 1563 | 1610 ±5% | +3.0% | 0.041 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 850 | 850 ±0% | ~ | 1.000 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 1541 | 1581 ±5% | ~ | 0.310 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 1755 | 1821 ±5% | +3.8% | 0.026 |  |
| internal/store.MarshalTaskValue | B/op | 1864 | 1864 ±0% | ~ | 0.699 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 1.7 | 1.8 ±12% | ~ | 0.818 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
