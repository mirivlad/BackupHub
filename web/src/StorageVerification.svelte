<script lang="ts">
 import {onMount} from 'svelte';
 import VerificationHistory from './VerificationHistory.svelte';
 import {checkActive, checkDate, type CheckMode, type CheckSchedule, type VerificationRun} from './verification';

 type Checks = {schedule: CheckSchedule; runs: VerificationRun[]};
 let {id, name, canConfigure, canRun, request, refreshStorages}:{
  id: string;
  name: string;
  canConfigure: boolean;
  canRun: boolean;
  request: <T>(path: string, options?: RequestInit) => Promise<T>;
  refreshStorages: () => Promise<void>;
 } = $props();
 let data = $state<Checks>();
 let mode = $state<CheckMode>('metadata');
 let scheduleMode = $state<CheckMode>('metadata');
 let hours = $state<number | undefined>(0);
 let busy = $state(false);
 let loading = $state(false);
 let error = $state('');
 let notice = $state('');
 const controller = new AbortController();
 const path = $derived('/storages/'+encodeURIComponent(id));
 const active = $derived(data?.runs.some(run => checkActive(run.state)) ?? false);
 const csrf = () => decodeURIComponent(document.cookie.split('; ').find(c => c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length) ?? '');

 async function refresh(){
  if (loading) return;
  loading = true;
  try {
   const next = await request<Checks>(path+'/checks', {signal: controller.signal});
   if (!data) {
    scheduleMode = next.schedule.mode;
    hours = next.schedule.interval_hours;
   }
   data = next;
  } catch (e) {
   if (!controller.signal.aborted) error = e instanceof Error ? e.message : 'История проверок недоступна';
  } finally {
   loading = false;
  }
 }

 async function submit(operation: 'checks' | 'check-schedule'){
  busy = true;
  error = '';
  notice = '';
  try {
   const schedule = operation === 'check-schedule';
   await request(path+'/'+operation, {
    method: schedule ? 'PUT' : 'POST',
    headers: {'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},
    body: JSON.stringify(schedule ? {mode:scheduleMode,interval_hours:hours} : {mode}),
    signal: controller.signal
   });
   notice = schedule ? 'Расписание сохранено.' : 'Проверка поставлена в очередь.';
   await Promise.all([refresh(), refreshStorages()]);
  } catch (e) {
   if (!controller.signal.aborted) error = e instanceof Error ? e.message : 'Операция не выполнена';
  } finally {
   busy = false;
  }
 }

 onMount(() => {
  void refresh();
  const poll = setInterval(() => {
   if (!busy) void refresh();
  }, 4000);
  return () => {clearInterval(poll); controller.abort();};
 });
</script>

<div class="verification-panel">
 <h3>Проверки · {name}</h3>
 {#if canRun}
  <form onsubmit={event => {event.preventDefault(); void submit('checks');}}>
   <label>Режим ручной проверки
    <select bind:value={mode}>
     <option value="metadata">Метаданные</option>
     <option value="data">Все данные</option>
    </select>
   </label>
   <button type="submit" disabled={!data || busy || active}>Проверить хранилище</button>
  </form>
 {/if}
 {#if canConfigure && data}
  <form onsubmit={event => {event.preventDefault(); void submit('check-schedule');}}>
   <label>Режим по расписанию
    <select bind:value={scheduleMode}>
     <option value="metadata">Метаданные</option>
     <option value="data">Все данные</option>
    </select>
   </label>
   <label>Интервал в часах (0 — выключено)
    <input type="number" min="0" max="8760" step="1" required bind:value={hours}/>
   </label>
   <button class="outline" type="submit" disabled={busy}>Сохранить расписание</button>
  </form>
 {/if}
 {#if data?.schedule.next_run}
  <p class="secondary">Следующий запуск {checkDate(data.schedule.next_run)}</p>
 {:else if data}
  <p class="secondary">Плановые проверки выключены.</p>
 {/if}
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 {#if notice}<p class="notice" role="status">{notice}</p>{/if}
 {#if data}<VerificationHistory runs={data.runs}/>{:else if loading}<p class="secondary">Загрузка проверок…</p>{/if}
</div>

<style>
 .verification-panel{border:1px solid var(--line);border-radius:9px;padding:14px;margin-bottom:20px}
 .verification-panel h3{margin-top:0}
 form{display:flex;align-items:end;gap:10px;flex-wrap:wrap;margin:0 0 12px}
 label{display:grid;gap:6px}
 input,select{padding:8px;border:1px solid var(--line);border-radius:7px;background:#fff;color:var(--text)}
 button{min-height:36px}
</style>
