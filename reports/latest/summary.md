## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 445 | 0 | 0 | 3 | 370s |
| macOS race | 447 | 0 | 0 | 1 | 400s |
| Linux coverage | 460 | 0 | 0 | 3 | 196s |

**Coverage:** 82.9% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 100848 | 98989 ±1% | ~ | 0.240 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11311 | 11309 ±0% | ~ | 0.818 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 58.4 | 58.5 ±4% | ~ | 0.937 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 113869 | 113318 ±2% | ~ | 0.485 | 600000 |
| internal/engine.AddAndGrant | B/op | 19346 | 19318 ±1% | ~ | 0.818 |  |
| internal/engine.AddAndGrant | allocs/op | 189 | 190 ±0% | ~ | 0.485 | 240 |
| internal/engine.GrantCycle | ns/op | 99406 | 100155 ±5% | ~ | 0.240 | 500000 |
| internal/engine.GrantCycle | B/op | 16807 | 16515 ±4% | ~ | 1.000 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 176 ±1% | ~ | 0.180 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2042 | 2002 ±1% | -2.0% 🟢 | 0.002 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 867 | 868 ±0% | ~ | 0.394 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2121 | 2116 ±8% | ~ | 0.699 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 2375 | 2391 ±3% | ~ | 0.394 |  |
| internal/store.MarshalTaskValue | B/op | 1864 | 1864 ±0% | ~ | 0.699 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 2.4 | 2.5 ±2% | ~ | 0.699 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
