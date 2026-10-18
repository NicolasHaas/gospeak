# Monitoring and Capacity

GoSpeak exposes an optional Prometheus endpoint and ships a Prometheus/Grafana
Compose overlay. Metrics are disabled by default. Enable the endpoint with
`-metrics :9602`, and keep this plaintext endpoint on a trusted network or bind
it to loopback.

## Bundled monitoring stack

The bundled overlay enables metrics only on the Compose network. It does not
publish Prometheus or port 9602 on the host:

```bash
GOSPEAK_METRICS_ADDR=:9602 \
GRAFANA_ADMIN_PASSWORD='<choose-password>' \
docker compose -f compose.yaml -f compose.monitoring.yaml up -d
```

Grafana is available on `127.0.0.1:3000`. The bundled dashboard shows current
capacity claims and startup-lifetime high-water utilization for:

- pre-authentication admission;
- authentication attempts;
- account provisioning;
- authenticated sessions;
- per-session, per-account, and server-wide control-message budgets;
- screen authentication and malformed-packet rejections.

## Capacity semantics

Authentication usage is the sum of failed attempts and in-flight checks
against the 30-attempt, one-minute source window. Account-provisioning usage is
the sum of successful creations and in-flight reservations against the
120-account, one-hour source window. These limits and pre-authentication
admission use one IPv4 address or one IPv6 /64 as the source group. IPv4-mapped
IPv6 uses the same group as IPv4. People sharing a /64 share these budgets,
including the default eight simultaneous pre-authentication connections per
plane. IP bans still match only the selected exact address. Trackers remain
bounded and reject new groups while full; /64 grouping does not prevent
exhaustion by attackers controlling many prefixes.

Each account may hold up to eight of the server's 1,024 authenticated session
slots by default. Pending session reservations count toward capacity but are
reported separately from active authenticated sessions.

One IPv4 address or IPv6 /64 may hold up to 64 active and pending sessions
across all accounts. IPv4-mapped IPv6 shares the IPv4 limit. This accommodates
64 users with one session each behind shared NAT; multiple sessions per user
share that allowance. Set `-max-sessions-per-source` higher when a trusted NAT
needs more. Non-positive values use 64. The cap bounds one source's occupancy,
not fair service across sources or protection against attackers with many
prefixes. Exact-address IP bans and `-open` authentication are unchanged.

Source capacity denial returns error code 3 without committing a new account
or consuming an invite. Disconnect and failed session preparation release the
claim; reconnect must claim capacity again. Empty source entries are removed.
`gospeak_session_source_capacity_max`,
`gospeak_session_source_capacity_high_water`, `gospeak_session_source_limit`,
and `gospeak_session_active_sources` expose source pressure without addresses.
`gospeak_session_rejections_total{reason="source"}` counts denials. Global and
per-account session limits still apply.

Control messages are charged against three token buckets:

| Scope | Default burst | Default refill |
|-------|---------------|----------------|
| Session | 60 points | 20 points/second |
| Account aggregate | 60 points | 20 points/second |
| Server-wide | 300 points | 100 points/second |

Message costs are weighted by work:

- ordinary mutations cost one point;
- chat costs two points;
- channel lists, joins, state and screen-share fanout, database work, exports,
  and server-wide changes cost five points;
- encoded size can raise the cost by one point per 16 KiB.

Configured bursts are never normalized below five points. Exhausting a session
or account bucket returns an error and disconnects that sender. Exhausting the
shared server bucket refuses only the message: requests receive error code 8,
while heartbeats are silently dropped without closing the connection. Local
budgets still charge refused messages. A depleted account bucket survives
reconnects until it refills, so reconnecting cannot restore its burst.

When the ordinary server-wide bucket is empty, authenticated kick and ban
requests with current server-owned role permission may use a shared 20-point
reserve, refilled at five points per second. This permits four normal requests
in a burst and one per second; encoded size can raise their cost. Account and
session budgets still charge first. Target permissions, self-action and
bootstrap-owner checks still apply; invalid role-qualified attempts also pay.
The reserve excludes other traffic and administration, survives reconnects,
and uses the same error/retry contract when empty. Global-budget metrics
measure the ordinary bucket. The reserve provides bounded moderation recovery.
Ordinary accounts and sources continue to share the server-wide bucket.

## Rejections, labels, and logs

Rejection metrics use bounded reason and scope labels. They never expose source
addresses, user IDs, usernames, session IDs, parser errors, or other
attacker-controlled values as Prometheus labels. Every rejection increments
its counter, including events whose warning log is suppressed.

Operational warning logs emit immediately and then at most once per ten
seconds for each bounded limit/reason class. The next emitted message includes
the number of suppressed repeats. This preserves visibility without allowing
rejection traffic to create a logging-amplification path.

The dashboard's 75% yellow and 90% red thresholds are operational guidance,
not configured alerts. High-water values cover the current server process and
reset on restart.

## Relevant configuration

The principal capacity flags are:

- `-max-sessions`
- `-max-sessions-per-user`
- `-max-sessions-per-source`
- `-control-message-burst`
- `-control-messages-per-second`
- `-control-global-burst`
- `-control-global-messages-per-second`

See the [server configuration table](../README.md#server-configuration) for
defaults and the [deployment guide](../deploy/README.md) for service examples.
