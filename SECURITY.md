# Security policy

Hesper runs agents and shells on your Macs and relays traffic between them,
so security reports get priority.

## Reporting a vulnerability

Report privately through GitHub:
[Security → Report a vulnerability](https://github.com/derzierau/hesper/security/advisories/new).
Please do not open a public issue, pull request or discussion for it.

Include what you can:

- the component (`hesperd`, `hesperctl`, `hesper-relay`, Hesper.app,
  `install.sh`) and version or commit;
- the steps to reproduce, or a proof of concept;
- the impact as you see it.

You should get a first reply within a week. Once a fix is ready we publish an
advisory and credit you, unless you prefer not to be named.

## Scope

In scope: the code in this repository, including the relay protocol, device
keys and approval, the end-to-end channel, the direct path and the installer.

Out of scope: a relay deployment you do not operate, third-party
dependencies (report those upstream; tell us if Hesper is affected), and
attacks that need an already compromised Mac or account.

## Supported versions

Security fixes go to `main` and, once releases exist, the latest release.
