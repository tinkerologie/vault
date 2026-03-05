# Phase 5: Trust & Notifications — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Add four "wow factor" features that build user trust — Discord notifications, container health verification, 3-2-1 compliance tracking, and a disaster recovery guide page.

**Architecture:** Each feature is independent; they can be built and deployed in any order. Discord notifications wire into the existing `runner.sendNotification()` path. Health checks extend `engine/container.go`. 3-2-1 is purely frontend. DR page adds one backend endpoint + one Svelte page.

**Tech Stack:** Go backend (Chi router, SQLite, Docker SDK, `net/http` for Discord), Svelte 5 frontend (runes, `$state`, `$derived`).

---

### Task 1: Discord Webhook — Backend Sender

**Files:**

- Create: `internal/notify/discord.go`
- Test: `internal/notify/discord_test.go`

**Step 1: Write the failing test**

```go
// internal/notify/discord_test.go
package notify

import (
 "encoding/json"
 "io"
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestSendDiscord_Success(t *testing.T) {
 var received DiscordPayload
 srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodPost {
   t.Fatalf("expected POST, got %s", r.Method)
  }
  body, _ := io.ReadAll(r.Body)
  if err := json.Unmarshal(body, &received); err != nil {
   t.Fatalf("invalid JSON: %v", err)
  }
  w.WriteHeader(http.StatusNoContent)
 }))
 defer srv.Close()

 embed := DiscordEmbed{
  Title:       "✅ Backup Completed",
  Description: "My Job",
  Color:       ColorSuccess,
  Fields: []DiscordField{
   {Name: "Duration", Value: "5m 30s", Inline: true},
   {Name: "Size", Value: "1.2 GB", Inline: true},
  },
 }

 err := SendDiscord(srv.URL, embed)
 if err != nil {
  t.Fatalf("unexpected error: %v", err)
 }
 if len(received.Embeds) != 1 {
  t.Fatalf("expected 1 embed, got %d", len(received.Embeds))
 }
 if received.Embeds[0].Title != "✅ Backup Completed" {
  t.Errorf("unexpected title: %s", received.Embeds[0].Title)
 }
}

func TestSendDiscord_Timeout(t *testing.T) {
 // Server that never responds.
 srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  select {} // block forever
 }))
 defer srv.Close()

 err := SendDiscord(srv.URL, DiscordEmbed{Title: "test"})
 if err == nil {
  t.Fatal("expected timeout error")
 }
}

func TestSendDiscord_EmptyURL(t *testing.T) {
 err := SendDiscord("", DiscordEmbed{Title: "test"})
 if err != nil {
  t.Fatal("empty URL should be a no-op, not an error")
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/... -run TestSendDiscord -v`
Expected: FAIL — `SendDiscord` undefined, `DiscordEmbed` undefined

**Step 3: Write minimal implementation**

```go
// internal/notify/discord.go
package notify

import (
 "bytes"
 "encoding/json"
 "fmt"
 "net/http"
 "time"
)

// Discord embed color constants (decimal).
const (
 ColorSuccess = 5763719  // #57F287 green
 ColorWarning = 16776960 // #FFFF00 yellow
 ColorDanger  = 15548997 // #ED4245 red
 ColorInfo    = 5793266  // #5865F2 blurple
)

// DiscordField is one inline field in an embed.
type DiscordField struct {
 Name   string `json:"name"`
 Value  string `json:"value"`
 Inline bool   `json:"inline"`
}

// DiscordEmbed is a single rich embed.
type DiscordEmbed struct {
 Title       string         `json:"title"`
 Description string         `json:"description"`
 Color       int            `json:"color"`
 Fields      []DiscordField `json:"fields,omitempty"`
 Timestamp   string         `json:"timestamp,omitempty"`
 Footer      *struct {
  Text string `json:"text"`
 } `json:"footer,omitempty"`
}

// DiscordPayload is the top-level JSON sent to a Discord webhook.
type DiscordPayload struct {
 Embeds []DiscordEmbed `json:"embeds"`
}

// SendDiscord posts a rich embed to a Discord webhook URL.
// It is non-blocking-safe: uses a 10-second timeout and returns any error
// for the caller to log (but not fail the backup).
func SendDiscord(webhookURL string, embed DiscordEmbed) error {
 if webhookURL == "" {
  return nil
 }

 if embed.Timestamp == "" {
  embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
 }
 embed.Footer = &struct {
  Text string `json:"text"`
 }{Text: "Vault Backup Manager"}

 payload := DiscordPayload{Embeds: []DiscordEmbed{embed}}
 body, err := json.Marshal(payload)
 if err != nil {
  return fmt.Errorf("marshal discord payload: %w", err)
 }

 client := &http.Client{Timeout: 10 * time.Second}
 resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
 if err != nil {
  return fmt.Errorf("discord webhook: %w", err)
 }
 defer resp.Body.Close()

 if resp.StatusCode >= 400 {
  return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
 }
 return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/... -run TestSendDiscord -v`
Expected: PASS (3 tests)

**Step 5: Commit**

```bash
git add internal/notify/discord.go internal/notify/discord_test.go
git commit -m "feat: add Discord webhook notification sender"
```

---

### Task 2: Discord Webhook — Runner Integration

**Files:**

- Modify: `internal/runner/runner.go` (function `sendNotification`, ~line 941)

**Step 1: Write the failing test**

No new test file — this wires existing tested functions together. The integration is tested via the existing runner flow.

**Step 2: Modify `sendNotification` in runner.go**

After the existing Unraid notification switch block (~line 971), add Discord notification logic:

```go
// After the existing switch block for Unraid notifications, add:

// Discord notifications.
webhookURL, _ := r.db.GetSetting("discord_webhook_url", "")
discordPref, _ := r.db.GetSetting("discord_notify_on", "always")
if webhookURL != "" && discordPref != "never" {
 shouldSend := discordPref == "always" || (discordPref == "failure" && status != "completed")
 if shouldSend {
  duration := int(time.Since(jobStart).Seconds())
  embed := r.buildDiscordEmbed(job.Name, status, done, failed, sizeBytes, duration, failedNames)
  go func() {
   if err := notify.SendDiscord(webhookURL, embed); err != nil {
    log.Printf("runner: discord notification error: %v", err)
   }
  }()
 }
}
```

**Step 3: Add the `buildDiscordEmbed` helper**

Add this method to runner.go (after `sendNotification`):

```go
func (r *Runner) buildDiscordEmbed(jobName, status string, done, failed int, sizeBytes int64, durationSec int, failedNames []string) notify.DiscordEmbed {
 var title string
 var color int
 switch status {
 case "completed":
  title = "✅ Backup Completed"
  color = notify.ColorSuccess
 case "partial":
  title = "⚠️ Backup Partially Completed"
  color = notify.ColorWarning
 default:
  title = "❌ Backup Failed"
  color = notify.ColorDanger
 }

 // Format duration.
 durStr := formatDuration(durationSec)

 // Format size.
 sizeStr := formatSizeHuman(sizeBytes)

 // Format speed.
 var speedStr string
 if durationSec > 0 {
  speedStr = formatSizeHuman(sizeBytes/int64(durationSec)) + "/s"
 }

 fields := []notify.DiscordField{
  {Name: "Duration", Value: durStr, Inline: true},
  {Name: "Size", Value: sizeStr, Inline: true},
 }
 if speedStr != "" {
  fields = append(fields, notify.DiscordField{Name: "Speed", Value: speedStr, Inline: true})
 }
 fields = append(fields, notify.DiscordField{
  Name:   "Items",
  Value:  fmt.Sprintf("%d/%d succeeded", done, done+failed),
  Inline: true,
 })

 if len(failedNames) > 0 {
  names := strings.Join(failedNames, ", ")
  if len(names) > 200 {
   names = names[:200] + "..."
  }
  fields = append(fields, notify.DiscordField{
   Name:  "Failed Items",
   Value: names,
  })
 }

 return notify.DiscordEmbed{
  Title:       title,
  Description: jobName,
  Color:       color,
  Fields:      fields,
 }
}
```

**Step 4: Add helper formatters if not already present**

Check if `formatDuration` and `formatSizeHuman` already exist in runner.go. If not, add:

```go
func formatDuration(seconds int) string {
 if seconds < 60 {
  return fmt.Sprintf("%ds", seconds)
 }
 if seconds < 3600 {
  return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
 }
 return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}

func formatSizeHuman(bytes int64) string {
 const (
  kb = 1024
  mb = kb * 1024
  gb = mb * 1024
 )
 switch {
 case bytes >= gb:
  return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
 case bytes >= mb:
  return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
 case bytes >= kb:
  return fmt.Sprintf("%.0f KB", float64(bytes)/float64(kb))
 default:
  return fmt.Sprintf("%d B", bytes)
 }
}
```

**Important:** The `sendNotification` method currently doesn't receive `jobStart` or `failedNames`. We need to update the function signature:

Change: `func (r *Runner) sendNotification(job db.Job, status string, done, failed int, sizeBytes int64)`
To: `func (r *Runner) sendNotification(job db.Job, status string, done, failed int, sizeBytes int64, durationSec int, failedNames []string)`

And update the call site in `RunJob()` (~line 417):

```go
r.sendNotification(job, status, itemsDone, itemsFailed, totalSize, int(time.Since(jobStart).Seconds()), failedNames)
```

**Step 5: Run tests**

Run: `go test ./internal/runner/... -v -short`
Run: `go test ./internal/notify/... -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/runner/runner.go
git commit -m "feat: integrate Discord notifications into backup runner"
```

---

### Task 3: Discord Webhook — Test Endpoint

**Files:**

- Modify: `internal/api/handlers/settings.go`
- Modify: `internal/api/routes.go`

**Step 1: Add TestDiscordWebhook handler to settings.go**

```go
// TestDiscordWebhook sends a test message to the configured Discord webhook.
//
// POST /api/v1/settings/discord/test
func (h *SettingsHandler) TestDiscordWebhook(w http.ResponseWriter, r *http.Request) {
 var req struct {
  WebhookURL string `json:"webhook_url"`
 }
 if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
  respondError(w, http.StatusBadRequest, "invalid JSON")
  return
 }
 if req.WebhookURL == "" {
  respondError(w, http.StatusBadRequest, "webhook_url is required")
  return
 }

 embed := notify.DiscordEmbed{
  Title:       "🔔 Test Notification",
  Description: "Vault is connected to Discord!",
  Color:       notify.ColorInfo,
  Fields: []notify.DiscordField{
   {Name: "Status", Value: "Connection verified", Inline: true},
  },
 }
 if err := notify.SendDiscord(req.WebhookURL, embed); err != nil {
  respondError(w, http.StatusBadGateway, "Discord webhook failed: "+err.Error())
  return
 }
 respondJSON(w, http.StatusOK, map[string]string{"message": "Test notification sent"})
}
```

Add the import for `notify` to settings.go:

```go
"github.com/ruaandeysel/vault/internal/notify"
```

**Step 2: Register the route in routes.go**

Add inside the `/settings` route group (after the staging routes, ~line 94):

```go
r.Post("/discord/test", settingsH.TestDiscordWebhook)
```

**Step 3: Run tests**

Run: `go test ./internal/api/... -v -short`
Expected: PASS (or existing tests still pass)

**Step 4: Commit**

```bash
git add internal/api/handlers/settings.go internal/api/routes.go
git commit -m "feat: add Discord webhook test endpoint"
```

---

### Task 4: Discord Webhook — Settings UI

**Files:**

- Modify: `web/src/pages/Settings.svelte` (notifications tab section)
- Modify: `web/src/lib/api.js` (add `testDiscordWebhook`)

**Step 1: Add API function**

In `web/src/lib/api.js`, add to the api object after the staging section:

```js
// Discord
testDiscordWebhook: (webhookUrl) => request('POST', '/settings/discord/test', { webhook_url: webhookUrl }),
```

**Step 2: Add Discord state variables to Settings.svelte**

After the existing staging state variables (~line 47):

```js
// Discord state
let discordWebhookUrl = $state("");
let discordNotifyOn = $state("always");
let discordSaving = $state(false);
let discordTesting = $state(false);
```

**Step 3: Initialize Discord state from settings in onMount**

In the onMount callback, after `stagingOverrideInput = staging?.override || ''` (~line 69):

```js
discordWebhookUrl = s?.discord_webhook_url || "";
discordNotifyOn = s?.discord_notify_on || "always";
```

**Step 4: Add save and test functions**

After the `reconnectWebSocket` function (~line 93):

```js
async function saveDiscordSettings() {
  discordSaving = true;
  try {
    settings = await api.updateSettings({
      discord_webhook_url: discordWebhookUrl,
      discord_notify_on: discordNotifyOn,
    });
    showToast("Discord settings saved", "success");
  } catch (e) {
    showToast(e.message, "error");
  } finally {
    discordSaving = false;
  }
}

async function testDiscord() {
  if (!discordWebhookUrl) {
    showToast("Enter a webhook URL first", "error");
    return;
  }
  discordTesting = true;
  try {
    await api.testDiscordWebhook(discordWebhookUrl);
    showToast("Test notification sent to Discord!", "success");
  } catch (e) {
    showToast("Discord test failed: " + e.message, "error");
  } finally {
    discordTesting = false;
  }
}
```

**Step 5: Add Discord section to Notifications tab**

After the existing Unraid Notifications card (the closing `</div>` of the first card inside `{#if activeTab === 'notifications'}`), add:

```svelte
<!-- Discord Notifications -->
<div class="bg-surface-2 border border-border rounded-xl overflow-hidden mt-6">
  <div class="px-5 py-4 border-b border-border">
    <h3 class="text-base font-semibold text-text">Discord Notifications</h3>
    <p class="text-xs text-text-muted mt-1">Receive rich backup notifications in your Discord channel.</p>
  </div>
  <div class="px-5 py-4 space-y-4">
    <div>
      <label for="discord-webhook" class="block text-sm font-medium text-text mb-1.5">Webhook URL</label>
      <div class="flex gap-2">
        <input id="discord-webhook" type="text" bind:value={discordWebhookUrl}
          placeholder="https://discord.com/api/webhooks/..."
          class="flex-1 bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text placeholder:text-text-dim focus:outline-none focus:border-vault" />
        <button onclick={testDiscord} disabled={discordTesting || !discordWebhookUrl}
          class="px-3 py-2 text-sm font-medium bg-surface-3 border border-border text-text rounded-lg hover:bg-surface-4 transition-colors disabled:opacity-50 shrink-0">
          {#if discordTesting}
            <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
          {:else}
            Test
          {/if}
        </button>
      </div>
      <p class="text-xs text-text-dim mt-1">Create a webhook in Discord: Server Settings → Integrations → Webhooks</p>
    </div>
    <div>
      <label for="discord-notify" class="block text-sm font-medium text-text mb-1.5">Notify On</label>
      <select id="discord-notify" bind:value={discordNotifyOn}
        class="bg-surface-3 border border-border rounded-lg px-3 py-2 text-sm text-text focus:outline-none focus:border-vault">
        <option value="always">Always (success + failure)</option>
        <option value="failure">Failures only</option>
        <option value="never">Never</option>
      </select>
    </div>
    <button onclick={saveDiscordSettings} disabled={discordSaving}
      class="px-4 py-2 text-sm font-medium bg-vault text-white rounded-lg hover:bg-vault-dark transition-colors disabled:opacity-50">
      {discordSaving ? 'Saving...' : 'Save Discord Settings'}
    </button>
  </div>
</div>
```

**Step 6: Build and verify**

Run: `cd web && npm run build`
Expected: Build succeeds with no errors.

**Step 7: Commit**

```bash
git add web/src/pages/Settings.svelte web/src/lib/api.js
git commit -m "feat: add Discord webhook settings UI with test button"
```

---

### Task 5: Container Health Checks — Backend

**Files:**

- Modify: `internal/engine/container.go` (add `VerifyContainerHealth` function)
- Create: `internal/engine/container_health_test.go`

**Step 1: Write the failing test**

```go
// internal/engine/container_health_test.go
package engine

import (
 "testing"
 "time"
)

func TestHealthCheckResult_String(t *testing.T) {
 r := HealthCheckResult{
  ContainerName: "plex",
  Status:        "healthy",
  Duration:      3 * time.Second,
  Message:       "Docker HEALTHCHECK passed",
 }
 if r.ContainerName != "plex" {
  t.Errorf("unexpected name: %s", r.ContainerName)
 }
 if r.Status != "healthy" {
  t.Errorf("unexpected status: %s", r.Status)
 }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run TestHealthCheckResult -v`
Expected: FAIL — `HealthCheckResult` undefined

**Step 3: Implement health check in container.go**

Add after the `StartContainers` function (end of file):

```go
// HealthCheckResult describes the post-restart health of a container.
type HealthCheckResult struct {
 ContainerName string        `json:"container_name"`
 Status        string        `json:"status"` // "healthy", "running", "unhealthy", "failed"
 Duration      time.Duration `json:"duration_ms"`
 Message       string        `json:"message"`
}

// VerifyContainerHealth polls a container's state after restart to determine
// if it is healthy. It checks Docker HEALTHCHECK status, running state, and
// optionally exposed port connectivity. Timeout is per-container.
func VerifyContainerHealth(containerID, containerName string, timeout time.Duration) (*HealthCheckResult, error) {
 cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
 if err != nil {
  return nil, fmt.Errorf("creating docker client: %w", err)
 }
 defer cli.Close()

 ctx, cancel := context.WithTimeout(context.Background(), timeout)
 defer cancel()

 start := time.Now()
 ticker := time.NewTicker(2 * time.Second)
 defer ticker.Stop()

 for {
  select {
  case <-ctx.Done():
   return &HealthCheckResult{
    ContainerName: containerName,
    Status:        "failed",
    Duration:      time.Since(start),
    Message:       "Timed out waiting for healthy state",
   }, nil
  case <-ticker.C:
   inspect, err := cli.ContainerInspect(ctx, containerID)
   if err != nil {
    continue
   }

   state := inspect.State
   if state == nil {
    continue
   }

   // Container not running at all — immediate failure.
   if !state.Running {
    if state.Restarting {
     continue // still restarting, keep polling
    }
    return &HealthCheckResult{
     ContainerName: containerName,
     Status:        "failed",
     Duration:      time.Since(start),
     Message:       fmt.Sprintf("Container is %s (exit code %d)", state.Status, state.ExitCode),
    }, nil
   }

   // If container defines a HEALTHCHECK, wait for it.
   if state.Health != nil {
    switch state.Health.Status {
    case "healthy":
     return &HealthCheckResult{
      ContainerName: containerName,
      Status:        "healthy",
      Duration:      time.Since(start),
      Message:       "Docker HEALTHCHECK passed",
     }, nil
    case "unhealthy":
     return &HealthCheckResult{
      ContainerName: containerName,
      Status:        "unhealthy",
      Duration:      time.Since(start),
      Message:       "Docker HEALTHCHECK reports unhealthy",
     }, nil
    default:
     continue // "starting" — keep polling
    }
   }

   // No HEALTHCHECK defined — "running" is good enough.
   return &HealthCheckResult{
    ContainerName: containerName,
    Status:        "running",
    Duration:      time.Since(start),
    Message:       "Container is running (no HEALTHCHECK defined)",
   }, nil
  }
 }
}
```

**Step 4: Run tests**

Run: `go test ./internal/engine/... -run TestHealthCheck -v -short`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/engine/container.go internal/engine/container_health_test.go
git commit -m "feat: add container health check verification after restart"
```

---

### Task 6: Container Health Checks — Runner + WebSocket Integration

**Files:**

- Modify: `internal/runner/runner.go` (after container restart section)

**Step 1: Add health check phase after container restarts**

In `RunJob()`, after the stop_all restart block (~line 316) and before the status determination (~line 318), add:

```go
// Verify container health after restarts (informational — does not affect status).
if len(stoppedContainerIDs) > 0 {
 r.broadcast(map[string]any{
  "type":    "phase_message",
  "job_id":  jobID,
  "message": fmt.Sprintf("Verifying health of %d containers...", len(stoppedContainerIDs)),
 })

 var healthResults []map[string]any
 for _, id := range stoppedContainerIDs {
  // Find the container name from items.
  name := id
  for _, item := range items {
   if item.ItemID == id {
    name = item.ItemName
    break
   }
  }
  result, err := engine.VerifyContainerHealth(id, name, 60*time.Second)
  if err != nil {
   log.Printf("runner: health check error for %s: %v", name, err)
   continue
  }
  healthResults = append(healthResults, map[string]any{
   "name":    result.ContainerName,
   "status":  result.Status,
   "message": result.Message,
   "duration_ms": result.Duration.Milliseconds(),
  })
  r.broadcast(map[string]any{
   "type":        "container_health_check",
   "job_id":      jobID,
   "name":        result.ContainerName,
   "status":      result.Status,
   "message":     result.Message,
   "duration_ms": result.Duration.Milliseconds(),
  })
 }
 // Store health results in activity log.
 if len(healthResults) > 0 {
  r.logActivity("info", "health",
   fmt.Sprintf("Health check: %s", job.Name),
   structuredDetails(healthResults))
 }
}
```

Also add the same pattern for the per-item restart case. In the backup item loop, after the container is restarted (~step 6 in `backupItem` via ContainerHandler), the runner doesn't directly restart — the container handler does. We need to add health checks in the **stop_all** path only (where the runner controls restarts), since per-item restarts happen inside the handler.

For per-item mode, add health verification after the handler's `Backup()` returns. In `RunJob()`, after the successful `backupItem` call (~line 262), check if the item is a container and verify health:

```go
// After successful backup of a container item, verify health.
if backupErr == nil && item.ItemType == "container" && job.ContainerMode != "stop_all" {
 noStop := false
 if s, ok := settings["no_stop"].(bool); ok {
  noStop = s
 }
 if !noStop {
  go func(itemID, itemName string) {
   result, err := engine.VerifyContainerHealth(itemID, itemName, 60*time.Second)
   if err != nil {
    log.Printf("runner: health check error for %s: %v", itemName, err)
    return
   }
   r.broadcast(map[string]any{
    "type":        "container_health_check",
    "job_id":      jobID,
    "name":        result.ContainerName,
    "status":      result.Status,
    "message":     result.Message,
    "duration_ms": result.Duration.Milliseconds(),
   })
  }(item.ItemID, item.ItemName)
 }
}
```

**Step 2: Run tests**

Run: `go test ./internal/runner/... -v -short`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/runner/runner.go
git commit -m "feat: verify container health after backup restarts via WebSocket"
```

---

### Task 7: 3-2-1 Compliance Score — Frontend Component

**Files:**

- Create: `web/src/components/ComplianceBadge.svelte`

**Step 1: Create the component**

```svelte
<script>
  let { storage = [], jobs = [], replicationSources = [] } = $props()

  // --- 3 Copies ---
  // Count distinct storage destinations with at least one enabled job.
  let activeDestIds = $derived(new Set(
    jobs.filter(j => j.enabled).map(j => j.storage_dest_id)
  ))
  let activeDests = $derived(storage.filter(s => activeDestIds.has(s.id)))
  let copies = $derived(Math.min(activeDests.length + replicationSources.length, 3))
  let copiesMax = 3

  // --- 2 Media Types ---
  let mediaTypes = $derived.by(() => {
    const types = new Set()
    for (const s of activeDests) {
      if (s.type === 'local') types.add('disk')
      else types.add('network')
    }
    if (replicationSources.length > 0) types.add('network')
    return types
  })
  let media = $derived(Math.min(mediaTypes.size, 2))
  let mediaMax = 2

  // --- 1 Offsite ---
  let hasOffsite = $derived(
    activeDests.some(s => s.type !== 'local') || replicationSources.length > 0
  )
  let offsite = $derived(hasOffsite ? 1 : 0)
  let offsiteMax = 1

  // --- Overall ---
  let totalScore = $derived(copies + media + offsite)
  let maxScore = copiesMax + mediaMax + offsiteMax
  let pct = $derived(Math.round((totalScore / maxScore) * 100))
  let color = $derived(
    totalScore >= 5 ? 'text-success' :
    totalScore >= 3 ? 'text-warning' :
    'text-danger'
  )
  let bgColor = $derived(
    totalScore >= 5 ? 'bg-success/10 border-success/30' :
    totalScore >= 3 ? 'bg-warning/10 border-warning/30' :
    'bg-danger/10 border-danger/30'
  )

  let expanded = $state(false)
</script>

<div class="border rounded-xl mb-8 {bgColor}">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="px-5 py-4 flex items-center justify-between cursor-pointer" onclick={() => expanded = !expanded}>
    <div class="flex items-center gap-3">
      <h3 class="text-sm font-semibold text-text">3-2-1 Backup Rule</h3>
      <span class="text-xs px-2.5 py-1 rounded-full font-medium {color} {bgColor}">
        {totalScore}/{maxScore}
      </span>
    </div>
    <div class="flex items-center gap-3">
      <div class="flex gap-2">
        <span class="text-xs {copies >= 3 ? 'text-success' : 'text-text-dim'}">
          {copies >= 3 ? '✓' : '✗'} 3 copies
        </span>
        <span class="text-xs {media >= 2 ? 'text-success' : 'text-text-dim'}">
          {media >= 2 ? '✓' : '✗'} 2 media
        </span>
        <span class="text-xs {offsite >= 1 ? 'text-success' : 'text-text-dim'}">
          {offsite >= 1 ? '✓' : '✗'} 1 offsite
        </span>
      </div>
      <svg class="w-4 h-4 text-text-muted transition-transform {expanded ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
    </div>
  </div>
  {#if expanded}
    <div class="px-5 pb-4 space-y-3 border-t border-border/50 pt-3">
      <div class="text-xs text-text-muted space-y-1.5">
        <p><strong class="{copies >= 3 ? 'text-success' : 'text-warning'}">Copies ({copies}/{copiesMax}):</strong> {activeDests.length} storage destination{activeDests.length !== 1 ? 's' : ''}{replicationSources.length > 0 ? ` + ${replicationSources.length} replication` : ''}</p>
        <p><strong class="{media >= 2 ? 'text-success' : 'text-warning'}">Media Types ({media}/{mediaMax}):</strong> {[...mediaTypes].join(', ') || 'none'}</p>
        <p><strong class="{offsite >= 1 ? 'text-success' : 'text-warning'}">Offsite ({offsite}/{offsiteMax}):</strong> {hasOffsite ? 'Remote storage configured' : 'No remote storage'}</p>
      </div>
      {#if totalScore < maxScore}
        <div class="text-xs text-text-muted bg-surface-3 rounded-lg p-3">
          <p class="font-medium text-text mb-1">Suggestions:</p>
          {#if copies < 3}
            <p>• Add more storage destinations to increase your backup copies</p>
          {/if}
          {#if media < 2}
            <p>• Add a remote storage (SFTP, SMB, NFS) for media type diversity</p>
          {/if}
          {#if offsite < 1}
            <p>• Configure an offsite destination or set up replication for off-site protection</p>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>
```

**Step 2: Build to verify**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 3: Commit**

```bash
git add web/src/components/ComplianceBadge.svelte
git commit -m "feat: add 3-2-1 compliance score component"
```

---

### Task 8: 3-2-1 Compliance Score — Dashboard Integration

**Files:**

- Modify: `web/src/pages/Dashboard.svelte`

**Step 1: Import ComplianceBadge**

Add import at the top (~line 12):

```js
import ComplianceBadge from "../components/ComplianceBadge.svelte";
```

**Step 2: Add replication sources state**

After `let healthSummary = $state(null)` (~line 28):

```js
let replicationSources = $state([]);
```

**Step 3: Load replication sources in loadDashboard**

In the Promise.all block, add:

```js
api.listReplicationSources().catch(() => []),
```

And destructure the result (add `replSources` to the destructuring). Then assign:

```js
replicationSources = replSources || [];
```

**Step 4: Place ComplianceBadge in the dashboard**

After the HealthGauge block and before the Stats Grid, add:

```svelte
{#if jobs.length > 0}
  <ComplianceBadge {storage} {jobs} {replicationSources} />
{/if}
```

**Step 5: Build and verify**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 6: Commit**

```bash
git add web/src/pages/Dashboard.svelte
git commit -m "feat: show 3-2-1 compliance score on dashboard"
```

---

### Task 9: Disaster Recovery — Backend Endpoint

**Files:**

- Create: `internal/api/handlers/recovery.go`
- Modify: `internal/api/routes.go`

**Step 1: Create recovery handler**

```go
// internal/api/handlers/recovery.go
package handlers

import (
 "net/http"
 "time"

 "github.com/ruaandeysel/vault/internal/db"
)

// RecoveryHandler serves the disaster recovery plan.
type RecoveryHandler struct {
 db      *db.DB
 version string
}

// NewRecoveryHandler creates a RecoveryHandler.
func NewRecoveryHandler(database *db.DB, version string) *RecoveryHandler {
 return &RecoveryHandler{db: database, version: version}
}

// GetPlan compiles a disaster recovery plan from existing data.
//
// GET /api/v1/recovery/plan
func (h *RecoveryHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
 jobs, err := h.db.ListJobs()
 if err != nil {
  respondError(w, http.StatusInternalServerError, err.Error())
  return
 }

 storage, err := h.db.ListStorageDestinations()
 if err != nil {
  respondError(w, http.StatusInternalServerError, err.Error())
  return
 }

 // Build storage name lookup.
 storageNames := make(map[int64]string)
 for _, s := range storage {
  storageNames[s.ID] = s.Name
 }

 // Collect all items, latest restore points, and warnings.
 type stepItem struct {
  Name            string     `json:"name"`
  LastBackup      *time.Time `json:"last_backup"`
  StorageName     string     `json:"storage_name"`
  SizeBytes       int64      `json:"size_bytes"`
  HasRestorePoint bool       `json:"has_restore_point"`
 }

 type step struct {
  Step        int        `json:"step"`
  Title       string     `json:"title"`
  Description string     `json:"description"`
  Status      string     `json:"status"` // "ready", "warning", "not_available"
  Items       []stepItem `json:"items,omitempty"`
  TotalSize   int64      `json:"total_size,omitempty"`
 }

 var warnings []string
 var steps []step
 var totalProtected, totalUnprotected int

 containerItems := []stepItem{}
 vmItems := []stepItem{}
 folderItems := []stepItem{}

 for _, job := range jobs {
  if !job.Enabled {
   continue
  }
  items, err := h.db.GetJobItems(job.ID)
  if err != nil {
   continue
  }
  rps, err := h.db.ListRestorePoints(job.ID)
  if err != nil {
   rps = nil
  }

  var latestRP *db.RestorePoint
  if len(rps) > 0 {
   latestRP = &rps[0]
  }

  for _, item := range items {
   totalProtected++
   si := stepItem{
    Name:            item.ItemName,
    StorageName:     storageNames[job.StorageDestID],
    HasRestorePoint: latestRP != nil,
   }
   if latestRP != nil {
    si.LastBackup = &latestRP.CreatedAt
    si.SizeBytes = latestRP.SizeBytes / int64(len(items)) // approximate per-item
   }

   // Warn if last backup is older than 7 days.
   if latestRP == nil {
    warnings = append(warnings, item.ItemName+" has no restore points")
   } else if time.Since(latestRP.CreatedAt) > 7*24*time.Hour {
    warnings = append(warnings, item.ItemName+" last backed up "+latestRP.CreatedAt.Format("Jan 2")+" (>7 days ago)")
   }

   switch item.ItemType {
   case "container":
    containerItems = append(containerItems, si)
   case "vm":
    vmItems = append(vmItems, si)
   case "folder":
    folderItems = append(folderItems, si)
   }
  }
 }

 stepNum := 1

 // Step 1: Install Vault
 steps = append(steps, step{
  Step:        stepNum,
  Title:       "Install Vault Plugin",
  Description: "Install the Vault plugin from Community Applications and restore the database from your backup storage.",
  Status:      "ready",
 })
 stepNum++

 // Step 2: Restore Containers
 if len(containerItems) > 0 {
  var totalSize int64
  for _, c := range containerItems {
   totalSize += c.SizeBytes
  }
  status := "ready"
  if !containerItems[0].HasRestorePoint {
   status = "warning"
  }
  steps = append(steps, step{
   Step:        stepNum,
   Title:       fmt.Sprintf("Restore Containers (%d)", len(containerItems)),
   Description: "Restore all Docker container appdata from backup.",
   Status:      status,
   Items:       containerItems,
   TotalSize:   totalSize,
  })
  stepNum++
 }

 // Step 3: Restore VMs
 if len(vmItems) > 0 {
  var totalSize int64
  for _, v := range vmItems {
   totalSize += v.SizeBytes
  }
  status := "ready"
  if !vmItems[0].HasRestorePoint {
   status = "warning"
  }
  steps = append(steps, step{
   Step:        stepNum,
   Title:       fmt.Sprintf("Restore Virtual Machines (%d)", len(vmItems)),
   Description: "Restore VM disk images and configurations from backup.",
   Status:      status,
   Items:       vmItems,
   TotalSize:   totalSize,
  })
  stepNum++
 }

 // Step 4: Restore Folders
 if len(folderItems) > 0 {
  var totalSize int64
  for _, f := range folderItems {
   totalSize += f.SizeBytes
  }
  steps = append(steps, step{
   Step:        stepNum,
   Title:       fmt.Sprintf("Restore Folders (%d)", len(folderItems)),
   Description: "Restore custom folder backups (Flash Drive, shares, etc.).",
   Status:      "ready",
   Items:       folderItems,
   TotalSize:   totalSize,
  })
  stepNum++
 }

 // Check for unprotected items.
 // We'd need container/VM discovery for this, but that requires Docker/libvirt.
 // For now, use the health summary data if available.
 totalUnprotected = 0 // placeholder — the frontend can compute this from its own data

 result := map[string]any{
  "server_info": map[string]any{
   "vault_version":          h.version,
   "total_protected_items":  totalProtected,
   "total_unprotected_items": totalUnprotected,
  },
  "steps":        steps,
  "warnings":     warnings,
  "last_updated": time.Now().UTC(),
 }
 respondJSON(w, http.StatusOK, result)
}
```

Add import for `"fmt"` at the top.

**Step 2: Register route in routes.go**

After the replication routes (~line 124), add:

```go
recoveryH := handlers.NewRecoveryHandler(s.db, s.config.Version)
r.Get("/recovery/plan", recoveryH.GetPlan)
```

**Step 3: Add API function**

In `web/src/lib/api.js`, add:

```js
// Recovery
getRecoveryPlan: () => request('GET', '/recovery/plan'),
```

**Step 4: Run tests**

Run: `go test ./internal/api/... -v -short`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/api/handlers/recovery.go internal/api/routes.go web/src/lib/api.js
git commit -m "feat: add disaster recovery plan API endpoint"
```

---

### Task 10: Disaster Recovery — Frontend Page

**Files:**

- Create: `web/src/pages/Recovery.svelte`
- Modify: `web/src/App.svelte` (add route + nav item)

**Step 1: Create Recovery.svelte**

```svelte
<script>
  import { onMount } from 'svelte'
  import { navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { formatBytes, formatDate } from '../lib/utils.js'
  import Spinner from '../components/Spinner.svelte'

  let loading = $state(true)
  let plan = $state(null)
  let error = $state('')
  let containers = $state([])
  let vms = $state([])
  let protectedItems = $state(new Set())
  let expandedSteps = $state(new Set())

  onMount(async () => {
    try {
      const [p, cRes, vRes, jobs] = await Promise.all([
        api.getRecoveryPlan(),
        api.listContainers().catch(() => ({ items: [] })),
        api.listVMs().catch(() => ({ items: [] })),
        api.listJobs(),
      ])
      plan = p
      containers = cRes.items || []
      vms = vRes.items || []

      // Compute protected items from enabled jobs.
      const pSet = new Set()
      for (const job of (jobs || []).filter(j => j.enabled)) {
        try {
          const detail = await api.getJob(job.id)
          for (const item of (detail?.items || [])) {
            pSet.add(`${item.item_type}:${item.item_name}`)
          }
        } catch {}
      }
      protectedItems = pSet
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  })

  let unprotectedContainers = $derived(containers.filter(c => !protectedItems.has(`container:${c.name}`)))
  let unprotectedVMs = $derived(vms.filter(v => !protectedItems.has(`vm:${v.name}`)))
  let totalUnprotected = $derived(unprotectedContainers.length + unprotectedVMs.length)
  let totalItems = $derived(containers.length + vms.length)
  let readinessPct = $derived(totalItems > 0 ? Math.round(((totalItems - totalUnprotected) / totalItems) * 100) : 100)

  function toggleStep(step) {
    const s = new Set(expandedSteps)
    if (s.has(step)) s.delete(step)
    else s.add(step)
    expandedSteps = s
  }

  function statusColor(status) {
    return status === 'ready' ? 'text-success' : status === 'warning' ? 'text-warning' : 'text-danger'
  }

  function statusIcon(status) {
    if (status === 'ready') return '✓'
    if (status === 'warning') return '⚠'
    return '✗'
  }
</script>

<div>
  <div class="mb-8">
    <h1 class="text-2xl font-bold text-text">Recovery Guide</h1>
    <p class="text-sm text-text-muted mt-1">Your disaster recovery plan — what to do if your server dies.</p>
  </div>

  {#if loading}
    <Spinner text="Loading recovery plan..." />
  {:else if error}
    <div class="bg-danger/10 border border-danger/30 text-danger rounded-xl p-4">
      <p class="text-sm">{error}</p>
    </div>
  {:else if plan}
    <!-- Readiness Hero -->
    <div class="bg-surface-2 border border-border rounded-xl p-6 mb-8">
      <div class="flex items-center gap-6">
        <div class="relative w-20 h-20 shrink-0">
          <svg viewBox="0 0 100 100" class="w-full h-full -rotate-90">
            <circle cx="50" cy="50" r="40" fill="none" stroke="var(--color-border)" stroke-width="8" />
            <circle cx="50" cy="50" r="40" fill="none"
              stroke={readinessPct >= 80 ? 'var(--color-success)' : readinessPct >= 50 ? 'var(--color-warning)' : 'var(--color-danger)'}
              stroke-width="8" stroke-linecap="round"
              stroke-dasharray={2 * Math.PI * 40} stroke-dashoffset={2 * Math.PI * 40 * (1 - readinessPct / 100)}
              class="transition-all duration-1000" />
          </svg>
          <div class="absolute inset-0 flex items-center justify-center">
            <span class="text-lg font-bold text-text">{readinessPct}%</span>
          </div>
        </div>
        <div>
          <h2 class="text-lg font-semibold text-text">Recovery Readiness</h2>
          <p class="text-sm text-text-muted mt-1">
            {readinessPct === 100 ? 'All items are protected and backed up.' :
             readinessPct >= 80 ? 'Most items protected. Review warnings below.' :
             'Several items need attention.'}
          </p>
          <p class="text-xs text-text-dim mt-1">
            {plan.server_info?.total_protected_items || 0} items protected · Vault v{plan.server_info?.vault_version || '?'}
          </p>
        </div>
      </div>
    </div>

    <!-- Warnings -->
    {#if (plan.warnings?.length > 0) || totalUnprotected > 0}
      <div class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-8">
        <h3 class="text-sm font-semibold text-warning mb-2">Warnings</h3>
        <ul class="space-y-1">
          {#if totalUnprotected > 0}
            <li class="text-xs text-text-muted">• {totalUnprotected} item{totalUnprotected !== 1 ? 's' : ''} not included in any backup job</li>
          {/if}
          {#each (plan.warnings || []).slice(0, 10) as w}
            <li class="text-xs text-text-muted">• {w}</li>
          {/each}
        </ul>
        {#if totalUnprotected > 0}
          <button onclick={() => navigate('/jobs')} class="mt-3 text-xs text-vault hover:text-vault-dark font-medium">
            Configure backup jobs →
          </button>
        {/if}
      </div>
    {/if}

    <!-- Recovery Steps -->
    <div class="space-y-4">
      {#each (plan.steps || []) as step}
        <div class="bg-surface-2 border border-border rounded-xl overflow-hidden">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="px-5 py-4 flex items-center gap-4 cursor-pointer" onclick={() => toggleStep(step.step)}>
            <div class="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold shrink-0 {step.status === 'ready' ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'}">
              {step.step}
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <h3 class="text-sm font-semibold text-text">{step.title}</h3>
                <span class="text-xs {statusColor(step.status)}">{statusIcon(step.status)}</span>
              </div>
              <p class="text-xs text-text-muted mt-0.5">{step.description}</p>
            </div>
            {#if step.total_size}
              <span class="text-xs text-text-dim shrink-0">{formatBytes(step.total_size)}</span>
            {/if}
            <svg class="w-4 h-4 text-text-muted transition-transform shrink-0 {expandedSteps.has(step.step) ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
          </div>
          {#if expandedSteps.has(step.step) && step.items?.length > 0}
            <div class="px-5 pb-4 border-t border-border pt-3">
              <div class="space-y-2">
                {#each step.items as item}
                  <div class="flex items-center justify-between px-3 py-2 bg-surface-3 rounded-lg">
                    <div class="flex items-center gap-2 min-w-0">
                      <div class="w-2 h-2 rounded-full shrink-0 {item.has_restore_point ? 'bg-success' : 'bg-warning'}"></div>
                      <span class="text-sm text-text truncate">{item.name}</span>
                    </div>
                    <div class="flex items-center gap-3 text-xs text-text-dim shrink-0">
                      {#if item.last_backup}
                        <span>{formatDate(item.last_backup)}</span>
                      {:else}
                        <span class="text-warning">No backup</span>
                      {/if}
                      {#if item.size_bytes}
                        <span>{formatBytes(item.size_bytes)}</span>
                      {/if}
                      <span class="text-text-muted">{item.storage_name || '—'}</span>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- Unprotected Items -->
    {#if unprotectedContainers.length > 0 || unprotectedVMs.length > 0}
      <div class="bg-surface-2 border border-border rounded-xl mt-8">
        <div class="px-5 py-4 border-b border-border">
          <h3 class="text-base font-semibold text-text">Unprotected Items</h3>
          <p class="text-xs text-text-muted mt-0.5">These items are not included in any backup job.</p>
        </div>
        <div class="p-5 space-y-2">
          {#each unprotectedContainers as c}
            <div class="flex items-center gap-2 px-3 py-2 bg-danger/5 rounded-lg">
              <div class="w-2 h-2 rounded-full bg-danger shrink-0"></div>
              <span class="text-sm text-text">{c.name}</span>
              <span class="text-[10px] text-text-dim ml-auto">container</span>
            </div>
          {/each}
          {#each unprotectedVMs as v}
            <div class="flex items-center gap-2 px-3 py-2 bg-danger/5 rounded-lg">
              <div class="w-2 h-2 rounded-full bg-danger shrink-0"></div>
              <span class="text-sm text-text">{v.name}</span>
              <span class="text-[10px] text-text-dim ml-auto">vm</span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</div>
```

**Step 2: Add route in App.svelte**

Add import after existing page imports (~line 15):

```js
import Recovery from "./pages/Recovery.svelte";
```

Add nav item — insert before the Settings entry in the `nav` array (~line 41). Use a shield icon:

```js
{ path: '/recovery', label: 'Recovery', icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z' },
```

Add route case in the main content section (after the Replication route, ~line 181):

```svelte
{:else if getRoute() === '/recovery'}
  <Recovery />
```

**Step 3: Build and verify**

Run: `cd web && npm run build`
Expected: Build succeeds.

**Step 4: Commit**

```bash
git add web/src/pages/Recovery.svelte web/src/App.svelte
git commit -m "feat: add Disaster Recovery guide page with recovery readiness score"
```

---

### Task 11: Deploy and Verify All Phase 5 Features

**Files:** None (deployment/testing only)

**Step 1: Build and deploy**

Run: `make redeploy`
Expected: All tests pass, binary deploys, API endpoints verified.

**Step 2: Verify with Playwright**

- Open `http://192.168.20.21:24085` and verify:
  - Dashboard shows 3-2-1 Compliance Badge below Health Gauge
  - Settings → Notifications tab shows Discord section
  - Recovery page is accessible from sidebar
  - Recovery page loads with steps and readiness percentage

**Step 3: Commit all remaining changes**

```bash
git add -A
git commit -m "feat: complete Phase 5 — Discord notifications, health checks, 3-2-1 score, recovery guide"
```

---

### Task 12: Phase 5 Integration Test

**Step 1: Test Discord webhook end-to-end**

Using Playwright or curl:

```bash
curl -X POST http://192.168.20.21:24085/api/v1/settings/discord/test \
  -H "Content-Type: application/json" \
  -d '{"webhook_url": "YOUR_TEST_WEBHOOK_URL"}'
```

Expected: 200 OK, test message appears in Discord.

**Step 2: Verify health check messages during backup**

Run a backup job and watch WebSocket for `container_health_check` events. The containers should report "healthy" or "running" status after restart.

**Step 3: Verify 3-2-1 score accuracy**

- With 1 local storage + 0 replication: should show 1-2/6 (1 copy, 1 media, 0 offsite)
- With 1 local + 1 SFTP: should show 4-5/6 (2 copies, 2 media, 1 offsite)

**Step 4: Verify recovery plan data**

Navigate to `/recovery` and confirm:

- Steps list containers and VMs that have restore points
- Warnings show for items with stale or missing backups
- Readiness percentage matches protection coverage
