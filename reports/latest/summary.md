## aiculator CI: ✅ all checks passed

| Tests | Passed | Failed | Flaky | Skipped | Time |
|---|---:|---:|---:|---:|---:|
| Linux race | 397 | 0 | 0 | 3 | 336s |
| macOS race | 399 | 0 | 0 | 1 | 321s |
| Linux coverage | 412 | 0 | 0 | 3 | 186s |

**Coverage:** 82.3% of statements, unit and end-to-end tests together.

| Benchmark | Unit | Base | Head | Change | p | Budget |
|---|---|---:|---:|---:|---:|---:|
| internal/api.HTTPHeartbeat | ns/op | 149324 | 149233 ±1% | ~ | 0.937 | 400000 |
| internal/api.HTTPHeartbeat | B/op | 11344 | 11311 ±0% | -0.3% 🟢 | 0.015 |  |
| internal/api.HTTPHeartbeat | allocs/op | 112 | 112 ±0% | ~ | 1.000 | 140 |
| internal/auth.Verify | ns/op | 68.9 | 68.3 ±1% | -1.0% 🟢 | 0.026 | 2000 |
| internal/auth.Verify | B/op | 80.0 | 80.0 ±0% | ~ | 1.000 |  |
| internal/auth.Verify | allocs/op | 1.0 | 1.0 ±0% | ~ | 1.000 | 3.0 |
| internal/engine.AddAndGrant | ns/op | 183269 | 182065 ±11% | ~ | 0.589 | 600000 |
| internal/engine.AddAndGrant | B/op | 18643 | 18602 ±1% | ~ | 0.310 |  |
| internal/engine.AddAndGrant | allocs/op | 189 | 188 ±0% | -0.5% 🟢 | 0.015 | 240 |
| internal/engine.GrantCycle | ns/op | 173691 | 173416 ±2% | ~ | 1.000 | 500000 |
| internal/engine.GrantCycle | B/op | 16930 | 16960 ±0% | ~ | 0.589 |  |
| internal/engine.GrantCycle | allocs/op | 176 | 175 ±1% | ~ | 0.589 | 220 |
| internal/engine.HeartbeatWithWaiters | ns/op | 2714 | 2751 ±1% | +1.4% | 0.009 | 20000 |
| internal/engine.HeartbeatWithWaiters | B/op | 853 | 853 ±0% | ~ | 1.000 |  |
| internal/engine.HeartbeatWithWaiters | allocs/op | 5.0 | 5.0 ±0% | ~ | 1.000 | 12.0 |
| internal/store.MarshalTaskPointer | ns/op | 2928 | 2933 ±1% | ~ | 0.310 |  |
| internal/store.MarshalTaskPointer | B/op | 712 | 712 ±0% | ~ | 1.000 | 900 |
| internal/store.MarshalTaskPointer | allocs/op | 8.0 | 8.0 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | ns/op | 3318 | 3320 ±1% | ~ | 0.818 |  |
| internal/store.MarshalTaskValue | B/op | 1865 | 1865 ±0% | ~ | 1.000 |  |
| internal/store.MarshalTaskValue | allocs/op | 10.0 | 10.0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | ns/op | 3.1 | 3.1 ±0% | ~ | 0.818 |  |
| internal/vlog.Disabled | B/op | 0 | 0 ±0% | ~ | 1.000 |  |
| internal/vlog.Disabled | allocs/op | 0 | 0 ±0% | ~ | 1.000 |  |

Base and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.
