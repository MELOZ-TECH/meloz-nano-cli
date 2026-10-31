# Contributing

Thanks for the interest. This is an experimental project; small, focused PRs are welcome.

1. Fork and branch from `main`.
2. Build: `make bootstrap && make sign`.
3. Run `make test` before opening the PR.
4. If you touched the VM/guest path, run `make itest` on a real Mac (CI cannot run it).
5. Keep commits small and describe the observed behavior you are changing.

## Scope

The goal is a minimal Docker runtime for macOS. Features that add daemons or
dependencies (Kubernetes, Rosetta, GUI) are out of scope unless they can be
opt-in and off by default.
