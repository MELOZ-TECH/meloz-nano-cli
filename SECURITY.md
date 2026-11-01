# Security Policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please **do not** open a public issue. Report privately using GitHub's
[Report a vulnerability](https://github.com/MELOZ-TECH/nano-container/security/advisories/new)
flow, or email `security@meloz.tech`.

Include:

- affected version (`nanoc --version`)
- macOS version and chip
- steps to reproduce
- impact (what an attacker could do)

You should receive an answer within 7 days. Please allow time for a fix before
disclosing the issue publicly.

## Scope

`nano-container` runs a Linux VM with the current user's privileges and requires
the `com.apple.security.virtualization` entitlement. Reports about the VM
boundary, the guest init, the module loading path and the Docker socket proxy are
especially welcome.
