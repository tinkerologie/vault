<script>
  let { effects } = $props()

  const methods = { running: 'Libvirt running state', guest_agent: 'QEMU guest agent', tcp: 'TCP connection' }
</script>

<section class="mt-4 border-t border-border pt-3 text-sm" aria-label="VM restore effects">
  <h3 class="font-medium text-text">VM resources</h3>
  <p class="mt-1 break-all text-text-muted">{effects.name} · UUID {effects.uuid}</p>
  {#if effects.disks.length || effects.nvram}
    <div class="mt-2 overflow-x-auto">
      <table class="w-full text-left" aria-label="VM disk and NVRAM targets">
        <thead class="text-text-muted">
          <tr>
            <th scope="col" class="py-1 pr-3 font-medium">Resource</th>
            <th scope="col" class="py-1 pr-3 font-medium">Format</th>
            <th scope="col" class="py-1 pr-3 font-medium">Destination</th>
            <th scope="col" class="py-1 font-medium">Action</th>
          </tr>
        </thead>
        <tbody class="text-text">
          {#each effects.disks as disk (disk.target)}
            <tr class="border-t border-border">
              <th scope="row" class="py-1 pr-3 font-medium">{disk.target}</th>
              <td class="py-1 pr-3">{disk.format}</td>
              <td class="py-1 pr-3 [overflow-wrap:anywhere]">{disk.destination}</td>
              <td class="py-1">{disk.replace ? 'Overwrite' : 'Create'}</td>
            </tr>
          {/each}
          {#if effects.nvram}
            <tr class="border-t border-border">
              <th scope="row" class="py-1 pr-3 font-medium">NVRAM</th>
              <td class="py-1 pr-3">Firmware variables</td>
              <td class="py-1 pr-3 [overflow-wrap:anywhere]">{effects.nvram.destination}</td>
              <td class="py-1">{effects.nvram.replace ? 'Overwrite' : 'Create'}</td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  {/if}
  {#if effects.unsupported_disks?.length}
    <p class="mt-2 text-warning">Disk data unavailable for {effects.unsupported_disks.map(disk => `${disk.target} (${disk.source_type})`).join(', ')}.</p>
  {/if}
  <ul class="mt-2 list-disc space-y-1 pl-5 text-text-muted">
    {#if effects.domain.stop}
      <li>Vault will shut down the existing VM and may force-stop it.</li>
    {/if}
    {#if effects.domain.replace}
      <li>Vault will remove the existing domain definition. Current state is {effects.domain.state}.</li>
    {/if}
    {#if effects.domain.remove_managed_save}
      <li>Vault will remove the existing managed save.</li>
    {/if}
    {#each effects.domain.may_remove_nvram ?? [] as path (path)}
      <li class="[overflow-wrap:anywhere]">Vault may remove existing NVRAM at {path}.</li>
    {/each}
    <li>Vault will define the VM from the saved configuration.</li>
    {#if effects.start_after_restore}
      <li>
        Vault will start the VM and check {methods[effects.readiness_mode]}.
        {#if effects.readiness_port}
          TCP port {effects.readiness_port} uses an {effects.readiness_address} address.
        {/if}
        {#if effects.readiness_mode !== 'running'}
          Guest readiness timeout is {effects.readiness_timeout_seconds} seconds.
        {/if}
      </li>
    {:else}
      <li>Vault will leave the VM stopped.</li>
    {/if}
  </ul>
</section>
