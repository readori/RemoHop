# Roadmap

RemoHop is open source (AGPL-3.0) first. Phase 1 aims at adoption: one binary that lets a person or an AI agent reach and operate another machine safely, without setting up a network first. Paid team features come later (phase 2) and fund the project through the commercial license.

## Phase 1 (in progress)

| Priority | Area | What |
|---|---|---|
| P0 | AI agents | Scoped, expiring grants for MCP clients (allowed services, read-only paths, command allow-lists), per-action approval, audit and session recording, a kill switch, setup docs for common agent tools |
| P0 | Quick support | Double-click host app for Windows and macOS: ID and password shown large, approve each connection, see and end sessions |
| P1 | Unattended Windows | System service that can show and control the login screen, lock screen and UAC prompts; off by default |
| P1 | Desktop experience | Audio, multi-monitor, drag-and-drop file upload, published latency numbers |
| P2 | Networking | Works alongside Tailscale (uses a shared tailnet as a direct path); `remohop net` mesh for the devices of one account with names, access policy, subnet routes and exit nodes |
| Ongoing | Distribution | Signed builds, Homebrew, Scoop, winget, AUR, deb/rpm, install scripts, a Docker image for the rendezvous server |
| Ongoing | Mobile | Android and iOS controller apps (no store releases yet); Android as a controlled device later |

## Phase 2: teams (commercial license)

Kept here so phase 1 decisions don't block it:

- Address book shared by a team, groups and per-group permissions
- Single sign-on (OIDC/SAML) and SCIM provisioning
- Central audit log, session recording storage and retention policies
- Admin console for devices, grants and relay usage
- Managed rendezvous/relay with SLA

Phase 2 builds on phase 1 pieces (accounts and their signed device ledger, grants, audit log, relay access control) rather than replacing them.
