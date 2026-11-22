<script lang="ts">
 import {formatDuration} from './duration';
 import {checkDate, checkLabel, checkTone, restoreCheckLabel, type VerificationRun} from './verification';
 let {runs}:{runs: VerificationRun[]} = $props();
 const kinds = {metadata: 'Метаданные', data: 'Все данные', restore: 'Пробное восстановление'};
 const number = new Intl.NumberFormat('ru-RU');
</script>

{#if runs.length === 0}
 <p class="secondary">Проверок пока нет.</p>
{:else}
 <ul class="check-runs">
  {#each runs as run (run.id)}
   <li>
    <div class="check-run">
     <strong>{run.kind==='restore' ? (run.selection ? 'Проба выбранного пути' : 'Автовыборка') : kinds[run.kind]}</strong>
     <span class={'check-badge '+checkTone(run.state)}>{run.kind==='restore'?restoreCheckLabel(run.state):checkLabel(run.state)}</span>
     <time datetime={run.started_at ?? run.queued_at}>{checkDate(run.started_at ?? run.queued_at)}</time>
     {#if run.duration_ms !== undefined}<small>{formatDuration(run.duration_ms)}</small>{/if}
     {#if run.files !== undefined}<small>Файлов {number.format(run.files)} · {number.format(run.bytes ?? 0)} Б</small>{/if}
    </div>
    {#if run.selection}<small><code>{run.selection==='.'?'Корень снимка':run.selection}</code></small>{/if}
    {#if run.state === 'running' && run.progress !== undefined}
     <progress aria-label="Ход проверки" max="1" value={run.progress}></progress>
     <small>{Math.round(run.progress * 100)} %</small>
    {/if}
    {#if run.detail}
     <details open={run.kind==='restore'&&run.state==='failed'}><summary>Подробности</summary><pre>{run.detail}</pre></details>
    {/if}
   </li>
  {/each}
 </ul>
{/if}

<style>
 .check-runs{margin:10px 0 0;padding:0;list-style:none;max-height:380px;overflow:auto}
 .check-runs li{border-top:1px solid var(--line);padding:10px 0}
 .check-run{display:flex;align-items:center;gap:8px 14px;flex-wrap:wrap}
 .check-run time,.check-run small{color:var(--muted);font-size:12px}
 details{margin-top:7px;font-size:13px}
 summary{cursor:pointer;width:fit-content}
 pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--surface-alt,#f6f8fb);padding:10px;border-radius:7px}
 progress{width:min(200px,75%);margin:7px 8px 0 0;accent-color:#326bcc}
</style>
