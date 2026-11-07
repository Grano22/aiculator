## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 445 | 0 | 0 | 3 | 369s |
| macOS race | 447 | 0 | 0 | 1 | 345s |
| Linux coverage | 460 | 0 | 0 | 3 | 242s |

**Coverage:** 82.9% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 100717 | 100997 ±8% | ~ | 0.937 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11313 | 11309 ±0% | ~ | 0.394 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 63.4 | 63.0 ±23% | ~ | 0.818 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 127191 | 125731 ±10% | ~ | 0.310 | 600000 |
| internal/engine.AddAndGrant | B/op | 19543 | 19552 ±4% | ~ | 0.589 |  |
| internal/engine.AddAndGrant | allocs/op | 190 | 189 ±0% | ~ | 0.240 | 240 |
| internal/engine.GrantCycle | ns/op | 115684 | 117280 ±2% | ~ | 0.699 | 500000 |
| internal/engine.GrantCycle | B/op | 16601 | 16549 ±1% | ~ | 0.240 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 176 ±0% | ~ | 0.699 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2762 | 2747 ±4% | ~ | 1.000 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 869 | 869 ±0% | ~ | 1.000 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2687 | 2672 ±2% | ~ | 0.394 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 3000 | 3033 ±10% | ~ | 0.132 |  |
| internal/store.MarshalTaskValue | B/op | 1865 | 1865 ±0% | ~ | 0.699 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 2.4 | 2.4 ±3% | ~ | 0.485 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
