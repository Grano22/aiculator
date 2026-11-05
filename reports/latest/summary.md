## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 397 | 0 | 0 | 3 | 279s |
| macOS race | 399 | 0 | 0 | 1 | 352s |
| Linux coverage | 412 | 0 | 0 | 3 | 170s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 95720 | 94993 ±1% | ~ | 0.093 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11302 | 11301 ±0% | ~ | 0.937 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 57.9 | 58.0 ±20% | ~ | 0.937 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 107780 | 107903 ±11% | ~ | 0.589 | 600000 |
| internal/engine.AddAndGrant | B/op | 19277 | 19259 ±1% | ~ | 0.699 |  |
| internal/engine.AddAndGrant | allocs/op | 190 | 189 ±0% | ~ | 0.699 | 240 |
| internal/engine.GrantCycle | ns/op | 97103 | 97018 ±3% | ~ | 1.000 | 500000 |
| internal/engine.GrantCycle | B/op | 17171 | 17106 ±4% | ~ | 0.394 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 175 ±1% | ~ | 0.699 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 1946 | 1921 ±1% | ~ | 0.093 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 851 | 851 ±0% | ~ | 0.699 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2078 | 2079 ±11% | ~ | 0.818 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 2352 | 2347 ±1% | ~ | 0.394 |  |
| internal/store.MarshalTaskValue | B/op | 1864 | 1865 ±0% | ~ | 0.394 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 2.4 | 2.5 ±4% | ~ | 0.937 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
