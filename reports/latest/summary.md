## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 421 | 0 | 0 | 3 | 345s |
| macOS race | 423 | 0 | 0 | 1 | 278s |
| Linux coverage | 436 | 0 | 0 | 3 | 203s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 155358 | 155438 ±4% | ~ | 0.589 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11322 | 11335 ±0% | ~ | 0.589 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 69.4 | 69.2 ±18% | ~ | 0.699 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 182940 | 180460 ±8% | ~ | 0.485 | 600000 |
| internal/engine.AddAndGrant | B/op | 18661 | 18731 ±2% | ~ | 0.180 |  |
| internal/engine.AddAndGrant | allocs/op | 190 | 190 ±1% | ~ | 0.937 | 240 |
| internal/engine.GrantCycle | ns/op | 169999 | 171384 ±2% | ~ | 0.394 | 500000 |
| internal/engine.GrantCycle | B/op | 16922 | 16960 ±1% | ~ | 0.485 |  |
| internal/engine.GrantCycle | allocs/op | 176 | 176 ±1% | ~ | 0.065 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2718 | 2725 ±4% | ~ | 0.937 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 869 | 869 ±0% | ~ | 0.699 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2971 | 2939 ±10% | ~ | 0.485 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 3328 | 3328 ±6% | ~ | 0.937 |  |
| internal/store.MarshalTaskValue | B/op | 1865 | 1865 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 3.1 | 3.1 ±0% | ~ | 0.310 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
