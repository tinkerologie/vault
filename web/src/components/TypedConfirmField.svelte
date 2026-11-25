<script>
  // A typed confirmation placed inside a ConfirmDialog's own content, for
  // dialogs where the word is only needed for one of several choices (#689):
  // deleting a job or destination *with its backup files*. The host passes
  // `confirmDisabled={value !== word}` to the dialog. Enter confirms once the
  // word matches, as it does in ConfirmDialog's built-in field.
  let { value = $bindable(''), word = 'DELETE', onenter = () => {} } = $props()

  function onkeydown(e) {
    if (e.key !== 'Enter') return
    e.preventDefault()
    if (value === word) onenter()
  }
</script>

<label class="block mt-4 text-sm text-text">
  Type <strong class="font-mono font-semibold">{word}</strong> to confirm
  <input
    type="text"
    bind:value
    autocomplete="off"
    autocapitalize="off"
    spellcheck="false"
    {onkeydown}
    class="mt-1.5 w-full px-3 py-2 max-lg:min-h-11 text-base sm:text-sm font-mono bg-surface-3 border border-border rounded-lg text-text focus:outline-none focus:border-vault"
  />
</label>
