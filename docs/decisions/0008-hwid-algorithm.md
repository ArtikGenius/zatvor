# 0008: HWID algorithm (frozen forever)

Status: accepted, 2026-11-19 (spec §5.5, review R3.12). **Frozen: this algorithm must never change.**

## Context

Subscription providers limit the number of devices per subscription by the `x-hwid` header. A device is
identified only by that value. Zatvor must send a stable, non-reversible id that differs per provider so
providers cannot correlate users.

## Decision

```
x-hwid = first 32 hex characters of
         HMAC-SHA256(key = "zatvor-hwid-v1", message = machine_id || subscription_host)
```

- `machine_id`: Windows `MachineGuid`, macOS `IOPlatformUUID`, Linux `/etc/machine-id`.
- `subscription_host`: the host of the subscription URL.
- `||` is plain byte concatenation.
- The id is stable across reinstalls, cannot be reversed to the machine id, and differs per provider host.
- `x-device-os`, `x-ver-os`, `x-device-model` are sent by default and can be switched off; README documents
  "What Zatvor sends". Before the first request to a real subscription the app warns that it registers a
  device.

## Why it is frozen forever

Providers count each distinct `x-hwid` as one device slot. Any change to the key, the message, the
encoding or the truncation produces a new value for every installed user, so every existing subscription
would suddenly see a new device and users would hit the device limit with slots they cannot free. A
version bump is therefore not an option: a golden-vector test must pin the algorithm, and any future need
is met by a new, separate, opt-in mechanism, never by editing this one. The key string `zatvor-hwid-v1`
keeps its `v1` name for the same reason.

## Consequences

- Stage 1 implements it exactly and adds a golden-vector test that fails on any change.
- A machine reinstall that keeps the machine id keeps its slot; a changed machine id (new OS install on
  Windows) is a new device, as with any HWID scheme.
- `subscription_host` is the lower-case hostname of the subscription URL (`url.Hostname()` lower-cased): no
  port, no path, no brackets around IPv6. `hwid.ID` does not normalise; callers pass that form. Frozen.

## Golden vector

```
machine_id        = 00000000-0000-0000-0000-000000000000
subscription_host = sub.example.com
x-hwid            = 32fe07fe2e30f9601fdac3197ad16506
```

Cross-checked with `printf '%s' "<machine_id><host>" | openssl dgst -sha256 -hmac zatvor-hwid-v1`. Pinned by
`TestHWIDVector` in `internal/hwid`.
