## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 397 | 0 | 0 | 3 | 338s |
| macOS race | 399 | 0 | 0 | 1 | 296s |
| Linux coverage | 412 | 0 | 0 | 3 | 199s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 149588 | 149584 ±1% | ~ | 0.818 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11316 | 11318 ±0% | ~ | 0.818 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 68.5 | 69.0 ±2% | ~ | 0.180 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 188428 | 185464 ±2% | ~ | 0.589 | 600000 |
| internal/engine.AddAndGrant | B/op | 18768 | 18745 ±1% | ~ | 0.699 |  |
| internal/engine.AddAndGrant | allocs/op | 189 | 189 ±0% | ~ | 0.818 | 240 |
| internal/engine.GrantCycle | ns/op | 177226 | 176535 ±2% | ~ | 0.699 | 500000 |
| internal/engine.GrantCycle | B/op | 17052 | 16953 ±1% | ~ | 0.093 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 175 ±1% | ~ | 0.699 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2736 | 2741 ±1% | ~ | 0.818 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 853 | 854 ±0% | ~ | 0.699 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2955 | 2940 ±1% | ~ | 0.394 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 3338 | 3329 ±0% | ~ | 0.310 |  |
| internal/store.MarshalTaskValue | B/op | 1865 | 1865 ±0% | ~ | 0.818 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 3.1 | 3.1 ±2% | ~ | 0.093 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
