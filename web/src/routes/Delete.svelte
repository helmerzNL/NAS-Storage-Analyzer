<script lang="ts">
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatNumber } from '../lib/format';
  import { prefs, setPref } from '../lib/session.svelte';
  import { MAX_SIMPLE, isDeleteScripts, type DeleteScripts } from '../lib/script';

  // Settings, Delete: which delete script the lists ask for when files and folders are ticked. The advanced script is the one the application
  // always wrote; the simple one is a .txt of commands to read, cut down and paste into a terminal (ADR-0011). The choice is a preference of the
  // signed-in administrator, like the units and the theme, and the script dialog says which one it is making.
  const kinds: { value: DeleteScripts; title: string; text: string }[] = [
    { value: 'advanced', title: 'delete.advanced', text: 'delete.advanced.text' },
    { value: 'simple', title: 'delete.simple', text: 'delete.simple.text' },
  ];

  // What a line of a simple script looks like, in the three languages. These are the commands themselves, so they are not translated.
  const examples: [string, string][] = [
    ['delete.example.sh', `rm -f -- '/mnt/user/media/films/Heat (1995).mkv'\nrm -rf -- '/mnt/user/media/series/Old Show'`],
    ['delete.example.powershell', `Remove-Item -LiteralPath '\\\\nas\\media\\films\\Heat (1995).mkv' -Force\nRemove-Item -LiteralPath '\\\\nas\\media\\series\\Old Show' -Recurse -Force`],
    ['delete.example.cmd', `del /f /q "\\\\nas\\media\\films\\Heat (1995).mkv"\nrd /s /q "\\\\nas\\media\\series\\Old Show"`],
  ];

  function choose(v: string) { if (isDeleteScripts(v)) void setPref('delete_scripts', v); }
</script>

<div class="page-head"><h1>{t('nav.delete')}</h1></div>
<p class="muted intro">{t('delete.intro')}</p>

<section class="card stack">
  <fieldset class="stack">
    <legend>{t('delete.kind')}</legend>
    {#each kinds as k (k.value)}
      <div class="stack" style="gap:2px">
        <label class="checkrow"><input type="radio" name="delete-scripts" value={k.value} checked={prefs.deleteScripts === k.value} onchange={() => choose(k.value)} aria-describedby={'delete-text-' + k.value} data-testid={'delete-' + k.value} />
          <b>{t(k.title)}</b></label>
        <span class="hint" id={'delete-text-' + k.value} style="margin-inline-start:28px">{t(k.text, { max: formatNumber(MAX_SIMPLE, i18n.lang) })}</span>
      </div>
    {/each}
  </fieldset>
  <span class="hint">{t('delete.account')}</span>
</section>

<section class="card stack" data-testid="delete-simple-info">
  <h2>{t('delete.simple')}</h2>
  <div class="notice warn small" style="margin:0">{t('delete.simple.warn')}</div>
  <h3>{t('delete.example')}</h3>
  {#each examples as [label, text] (label)}
    <div class="field">
      <span>{t(label)}</span>
      <code class="codeblock mono">{text}</code>
    </div>
  {/each}
</section>
