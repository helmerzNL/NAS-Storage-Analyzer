<script lang="ts">
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { session, prefs, setPref } from '../lib/session.svelte';
  import AccentPicker from '../components/AccentPicker.svelte';

  const user = $derived(session.user);
</script>

<div class="page-head"><h1>{t('settings.appearance')}</h1></div>
<p class="muted intro">{user?.username} · {t('role.' + user?.role)} · {t('account.lastlogin', { when: formatDateTime(user?.last_login_at, i18n.lang) })}</p>

<section class="card stack">
  <h2>{t('account.prefs')}</h2>
  <label class="field">{t('pref.units')}
    <select value={prefs.units} onchange={(e) => setPref('units', e.currentTarget.value)} data-testid="pref-units"><option value="binary">{t('pref.units.binary.long')}</option><option value="decimal">{t('pref.units.decimal.long')}</option></select></label>
  <label class="field">{t('pref.language')}
    <select value={i18n.lang} onchange={(e) => setPref('language', e.currentTarget.value)} data-testid="pref-language"><option value="nl">Nederlands</option><option value="en">English</option></select></label>
  <label class="field">{t('pref.theme')}
    <select value={prefs.theme} onchange={(e) => setPref('theme', e.currentTarget.value)} data-testid="pref-theme"><option value="system">{t('pref.theme.system')}</option><option value="light">{t('pref.theme.light')}</option><option value="dark">{t('pref.theme.dark')}</option></select></label>
  <AccentPicker />
</section>
