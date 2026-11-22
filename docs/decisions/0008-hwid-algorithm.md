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
- Input normalisation is enforced in code by `hwid.ID` and is frozen:
  - `machine_id`: `strings.ToLower(strings.TrimSpace(machine_id))` on every OS.
  - `subscription_host`: `hwid.NormalizeHost`. Accepts `host`, `host:port`, `[v6]`, `[v6]:port` or a bare IPv6
    address; lower-cases; strips port, brackets, an IPv6 zone (`%eth0`) and one trailing dot; converts IDN to
    punycode with `golang.org/x/net/idna` Lookup profile (on error the lower-cased input is kept).
  - Vectors: `Sub.Example.COM:8443` -> `sub.example.com`; `sub.example.com.` -> `sub.example.com`;
    `[2001:DB8::1]:443` -> `2001:db8::1`; `fe80::1%eth0` -> `fe80::1`; `пример.рф` -> `xn--e1afmkfd.xn--p1ai`.

## Golden vector

```
machine_id        = 00000000-0000-0000-0000-000000000000
subscription_host = sub.example.com
x-hwid            = 32fe07fe2e30f9601fdac3197ad16506
```

Cross-checked with `printf '%s' "<machine_id><host>" | openssl dgst -sha256 -hmac zatvor-hwid-v1`. Pinned by
`TestHWIDVector` in `internal/hwid`.

Machine id normalisation vector:

```
machine_id (raw)  = "  00000000-0000-0000-0000-00000000ABCD\n"
normalised        = 00000000-0000-0000-0000-00000000abcd
subscription_host = sub.example.com
x-hwid            = 91b5d68392869f6d1f5aa4038b2c0289
```
