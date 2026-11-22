<script lang="ts">
 import {onMount} from 'svelte';
 import VerificationHistory from './VerificationHistory.svelte';
 import {checkActive, checkDate, checkLabel, checkTone, type CheckStatus, type VerificationRun} from './verification';

 let {id, status, canRun}:{id: string; status: CheckStatus; canRun: boolean} = $props();
 let runs = $state<VerificationRun[]>([]);
 let busy = $state(false);
 let loading = $state(false);
 let loaded = $state(false);
 let showHistory = $state(false);
 let error = $state('');
 const controller = new AbortController();
 const path = $derived('/api/v1/backup/snapshots/'+encodeURIComponent(id)+'/restore-checks');
 const latest = $derived(runs[0]);
 const restoreState = $derived(latest && !(status.state === 'stale' && latest.finished_at === status.finished_at)
  ? latest.state : status.state);
 const checkedAt = $derived(latest ? latest.finished_at : status.finished_at);
 const active = $derived(checkActive(restoreState));
 const csrf = () => decodeURIComponent(document.cookie.split('; ').find(c => c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length) ?? '');

 async function refresh(){
  if (loading) return;
  loading = true;
  try {
   const response = await fetch(path,{credentials:'same-origin',signal:controller.signal});
   const body = await response.json();
   if (!response.ok) throw new Error(body.error ?? 'История проверок недоступна');
   runs = body.runs;
   loaded = true;
  } catch(e) {
   if (!controller.signal.aborted) error = e instanceof Error ? e.message : 'История проверок недоступна';
  } finally {
   loading = false;
  }
 }

 async function start(){
  busy = true;
  error = '';
  try {
   const response = await fetch(path,{
    method:'POST',credentials:'same-origin',signal:controller.signal,
    headers:{'X-BackupHub-CSRF':csrf()}
   });
   const body = await response.json();
   if (!response.ok) throw new Error(body.error ?? 'Не удалось начать проверку');
   runs = [body as VerificationRun, ...runs];
   showHistory = true;
   await refresh();
  } catch(e) {
   if (!controller.signal.aborted) error = e instanceof Error ? e.message : 'Не удалось начать проверку';
  } finally {
   busy = false;
  }
 }

 onMount(() => {
  const poll = setInterval(() => {if (showHistory || active) void refresh();}, 4000);
  return () => {clearInterval(poll); controller.abort();};
 });
</script>

<div class="snapshot-check">
 <div class="check-heading">
  <strong>Пробное восстановление</strong>
  <span class={'check-badge '+checkTone(restoreState)}>{checkLabel(restoreState)}</span>
  {#if checkedAt}<small>{checkDate(checkedAt)}</small>{/if}
  {#if canRun}<button type="button" class="outline" disabled={busy || active} onclick={()=>void start()}>Проверить восстановление</button>{/if}
  <button type="button" class="outline" aria-expanded={showHistory} onclick={()=>{showHistory=!showHistory;if(showHistory)void refresh();}}>История проверок</button>
 </div>
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 {#if showHistory}
  <p class="secondary">Проверяется выборка до 8 файлов и 256 МиБ.</p>
  {#if loaded || runs.length}<VerificationHistory {runs}/>{:else if loading}<p class="secondary">Загрузка проверок…</p>{/if}
 {/if}
</div>

<style>
 .snapshot-check{border:1px solid var(--line);border-radius:9px;padding:12px}
 .check-heading{display:flex;gap:9px 14px;align-items:center;flex-wrap:wrap}
 .check-heading small{color:var(--muted)}
</style>
