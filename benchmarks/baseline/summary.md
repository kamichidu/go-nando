# Interface benchmark summary

Scope: end-to-end adapter/protocol observations; cross-interface latency deltas do not isolate adapter-only overhead. No performance threshold is applied.

Conditions: 10 warmup iterations and 100 measured iterations per available interface; 4096-byte payload; unique paths; local delivery; localhost; host caches not cleared.

## fuse: completed

Successful 100, failed 0.

- create_write_ns: median 24690176.5 ns, p95 43493718 ns, p99 44635174 ns (100 samples)
- read_ns: median 4279833.0 ns, p95 10252388 ns, p99 23644476 ns (100 samples)
- stat_ns: median 661096.0 ns, p95 777022 ns, p99 791743 ns (100 samples)
- list_ns: median 1234017.0 ns, p95 1386557 ns, p99 1414998 ns (100 samples)
- rename_ns: median 4446578.0 ns, p95 6331104 ns, p99 20668615 ns (100 samples)
- delete_ns: median 2726226.0 ns, p95 5032316 ns, p99 20291381 ns (100 samples)
## rpc: completed

Successful 100, failed 0.

- create_write_ns: median 8761405.5 ns, p95 27265038 ns, p99 45751044 ns (100 samples)
- read_ns: median 3613977.0 ns, p95 5819495 ns, p99 44214528 ns (100 samples)
- stat_ns: median 659048.0 ns, p95 756961 ns, p99 832502 ns (100 samples)
- list_ns: median 661330.5 ns, p95 725535 ns, p99 772154 ns (100 samples)
- rename_ns: median 2557780.5 ns, p95 4715545 ns, p99 18239254 ns (100 samples)
- delete_ns: median 2096056.0 ns, p95 3939745 ns, p99 10204245 ns (100 samples)
## webdav: completed

Successful 100, failed 0.

- create_write_ns: median 9411953.5 ns, p95 27656904 ns, p99 41195833 ns (100 samples)
- read_ns: median 3519664.0 ns, p95 4869882 ns, p99 19006261 ns (100 samples)
- stat_ns: median 1427283.5 ns, p95 1552265 ns, p99 1688760 ns (100 samples)
- list_ns: median 2589440.5 ns, p95 3132140 ns, p99 3288760 ns (100 samples)
- rename_ns: median 4447802.0 ns, p95 16099326 ns, p99 23317081 ns (100 samples)
- delete_ns: median 2429548.0 ns, p95 3407054 ns, p99 22874596 ns (100 samples)
