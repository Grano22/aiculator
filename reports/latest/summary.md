## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 421 | 0 | 0 | 3 | 282s |
| macOS race | 423 | 0 | 0 | 1 | 427s |
| Linux coverage | 436 | 0 | 0 | 3 | 190s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 88763 | 87150 ±5% | ~ | 0.937 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11296 | 11299 ±0% | ~ | 0.065 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 54.8 | 54.7 ±3% | ~ | 0.818 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 106491 | 106762 ±3% | ~ | 0.699 | 600000 |
| internal/engine.AddAndGrant | B/op | 19249 | 19246 ±1% | ~ | 0.818 |  |
| internal/engine.AddAndGrant | allocs/op | 189 | 190 ±0% | ~ | 0.240 | 240 |
| internal/engine.GrantCycle | ns/op | 97955 | 95704 ±2% | ~ | 0.065 | 500000 |
| internal/engine.GrantCycle | B/op | 16454 | 17138 ±4% | ~ | 0.132 |  |
| internal/engine.GrantCycle | allocs/op | 175 | 175 ±1% | ~ | 0.937 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2357 | 2340 ±4% | ~ | 0.485 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 852 | 868 ±0% | +1.9% | 0.000 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2266 | 2267 ±6% | ~ | 0.818 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 2587 | 2584 ±9% | ~ | 0.699 |  |
| internal/store.MarshalTaskValue | B/op | 1864 | 1864 ±0% | ~ | 0.699 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 2.0 | 2.1 ±3% | ~ | 0.589 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
