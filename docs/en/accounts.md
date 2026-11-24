# Accounts (optional)

[中文](../zh/accounts.md)

An account only groups your own devices, so that each can list the others and see which are online (`remohop devices`). It grants no access: connecting to a host still needs a password, pairing token or key that the host accepts.

Accounts live on the rendezvous server. Its operator turns them on with `remohop signal --accounts` (SMTP and an allowed-address rule are also required; see `remohop help signal`).

```bash
remohop login me@example.com
remohop account
remohop account approve 3f9a:01bc:77d2:e410:9c55
remohop account remove old-laptop
remohop logout
```

- The server mails the login code: 8 digits, valid 10 minutes, single use.
- The first device to log in creates the account. Every further device must be approved by a device already in it.
- Joining needs a full-fingerprint comparison on both devices. The new device shows the **account fingerprint**: confirm it equals the one `remohop account` shows on your other device. It also prints **its own identity fingerprint**: on the other device run `remohop account approve FINGERPRINT` with the complete value. A prefix is refused.
- Membership is a hash-chained log signed by the account's own devices. The server stores and relays it but holds no member key, so it cannot add a device.
- Trusting account devices is opt-in per host. `remohop config account-trust on` (default off) makes a host accept the account's other devices like keys authorised with `remohop authorize`. The host re-checks the signed list about once a minute. After `remohop account remove`, hosts with the option stop accepting that device at their next check.
- A server can still refuse service, and can withhold the newest log entries (a removal, for example) from a device. A host that cannot reach the server keeps the last verified list for up to 7 days, then trusts no account device. Full discussion: [security.md](../security.md#accounts).
