# Interface benchmark baseline

Captured on 2026-11-18 from successful [GitHub Actions run 44409935224](https://github.com/kamichidu/go-nando/actions/runs/44409935224), using source SHA `5826d1d1b29c13d77f22a8519cb3d5c25c70ce5d`. Each interface used a 4 KiB payload, 10 warmup iterations, and 100 measured iterations. RPC, WebDAV, and FUSE all completed successfully; FUSE was mounted and measured.

The per-interface JSON files retain the original measured observations and summary statistics. Host-specific temporary endpoint, mountpoint, and checkout paths were removed. `manifest.json` and `summary.md` are from the artifact; `environment.json` records the execution environment without runner hostnames or paths.

These are end-to-end observations, not adapter-only costs. Hosted runner hardware, storage, caches, and shared workload vary; comparisons across runs are indicative rather than controlled. See `summary.md` for the operation summaries and `manifest.json` for completion counts.
