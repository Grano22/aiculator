## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 445 | 0 | 0 | 3 | 302s |
| macOS race | 447 | 0 | 0 | 1 | 438s |
| Linux coverage | 460 | 0 | 0 | 3 | 200s |

**Coverage:** 82.9% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 152251 | 153938 ±3% | ~ | 0.589 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11325 | 11324 ±0% | ~ | 0.699 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 69.4 | 71.2 ±21% | ~ | 0.180 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 194466 | 194956 ±8% | ~ | 1.000 | 600000 |
| internal/engine.AddAndGrant | B/op | 18670 | 18736 ±1% | ~ | 0.180 |  |
| internal/engine.AddAndGrant | allocs/op | 189 | 189 ±0% | ~ | 0.240 | 240 |
| internal/engine.GrantCycle | ns/op | 181676 | 181196 ±3% | ~ | 0.818 | 500000 |
| internal/engine.GrantCycle | B/op | 17135 | 17109 ±1% | ~ | 0.818 |  |
| internal/engine.GrantCycle | allocs/op | 176 | 176 ±1% | ~ | 0.589 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2880 | 2867 ±1% | ~ | 0.394 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 870 | 870 ±0% | ~ | 1.000 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2982 | 2994 ±2% | ~ | 0.937 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 3430 | 3405 ±1% | ~ | 0.485 |  |
| internal/store.MarshalTaskValue | B/op | 1864 | 1864 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 3.1 | 3.1 ±0% | ~ | 0.065 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
