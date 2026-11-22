# 0010: Provider routing is never rewritten

Status: accepted, 2026-11-22 (spec §6.1, §6.3; stage 1a rulings in `gen`).

## Context

A provider's JSON config can carry its own routing, inbounds and outbounds. Zatvor adds its own
inbounds (mixed proxy, probe, API) and rule layers. Rewriting the provider's rules would break what the
provider intended, especially Happ rule order.

## Decision

- Provider routing rules are never edited. Rule values, tags and order stay as the provider wrote them.
- Our mixed inbound takes the tag of the first provider inbound with protocol `socks`, `http` or `mixed`
  (otherwise the first provider inbound). Rules that name other provider inbound tags are left as they
  are. They will not match, and the build report lists those tags as a warning.
- Source block rules stay inside the source, in source order. Layer 2 holds user blocks only. Hoisting
  provider blocks would break Happ `RouteOrder`.
- `ProxyTag` (the outbound used for probes and tests) is the target of the first catch-all rule, since
  the first one shadows later ones. Without a catch-all it is the first outbound. If that target is a
  `freedom`, `blackhole`, `dns` or `loopback` outbound, the first outbound of any other protocol is used,
  so a probe never goes direct.

## Consequences

- A provider block rule placed after a proxy rule does not win. This follows the provider's own order.
- Rules for an inbound tag we do not use are inert but visible in the report.
- Revisit if a real provider config needs a different inbound choice.
