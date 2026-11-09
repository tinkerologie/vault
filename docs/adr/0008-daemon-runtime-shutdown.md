---
status: accepted
---

# Give daemon workers one shutdown owner

Daemon and replica commands share an API runtime that owns serving and
shutdown. The database, maintenance lock, and snapshot configuration belong
to the CLI. The runtime must return before the CLI releases those resources.
This extends [ADR 0006](0006-runtime-and-maintenance-exclusion.md).

## Startup

Server construction wires dependencies without starting workers. The CLI
starts the heartbeat immediately and defers its join across every exit,
including TLS and bind errors. The API validates and loads TLS credentials
and binds the listener before starting the WebSocket hub, mount sweeper,
scheduler, discovery, throttle, snapshot ticker, and anomaly workers. Replica
mode starts only the workers it uses. A serving error triggers the same
cleanup as cancellation and is returned to the caller.

## Shutdown

1. Cancel the worker context and stop scheduling new cron callbacks. Flush
   the configuration snapshot synchronously before draining the Runner.
2. Stop accepting HTTP requests and cancel MCP transport requests. Let existing cron
   callbacks finish dispatching claimed retries before closing Runner
   admission. REST and MCP reserve Runner admission before acknowledging
   background operations. Wait for active Runner tasks and HTTP handlers.
3. Close MCP sessions and join any restore-point listing that outlived its
   HTTP timeout. Join the mount sweeper and unmount active FUSE sessions.
   Join the FUSE serve loop before closing its adapter. Stop the WebSocket
   hub and join the connections, pumps, and eviction work.
4. Join manual replication, discovery, throttle and snapshot loops, and the
   anomaly trend ticker. Drain the anomaly evaluator after its producers
   finish. Join remaining Runner work, including storage cleanups, probes,
   finalization and stall watchdogs. Cancel pending adaptive rechecks.
5. Cancel queued configuration flushes and join any flush in progress.
   Write the final snapshot. Join the heartbeat before the CLI closes the
   database and releases the maintenance lock.

The HTTP listener cannot account for hijacked WebSockets. The hub owns
upgrades and both pumps. Stop releases blocked broadcast producers, and
repeated stop calls wait for the same cleanup.

## Limits

Cancellation asks workers to exit. It cannot interrupt a blocked local
filesystem call. After 30 seconds Vault logs that Runner drain is still in
progress and keeps waiting. HTTP and worker cleanup also wait for completion.
The existing `rc.vault` process escalation controls forced termination.
A FUSE session that fails to detach stays recorded for startup cleanup.

The runtime does not change backup, restore, replication, or storage
algorithms. Outbound Discord sends use already-built payloads and do not
read the database or write Vault storage after dispatch.
