<script lang="ts">
 import {onMount,tick} from 'svelte';
 import {formatDuration} from './duration';

 type Item={job_id:string;job_name:string;start:string;end?:string};
 type Slot={planned:number;overlap:boolean;unknown:boolean;past:boolean;observed_seconds:number;busy_seconds:number;items?:Item[]};
 type Day={date:string;weekday:number;hours:Slot[]};
 type Overview={timezone:string;days:Day[];planned_runs:number;overlap_hours:number;unmeasured_jobs:number;active_job:boolean;truncated:boolean;busy_seconds:number;observed_seconds:number;history_days:number;free_window?:{start:string;end:string}};
 let {onEditJob}:{onEditJob?:(id:string)=>void}=$props();
 const weekdays=['Воскресенье','Понедельник','Вторник','Среда','Четверг','Пятница','Суббота'];
 const compact=['Вс','Пн','Вт','Ср','Чт','Пт','Сб'];
 const hours=Array.from({length:24},(_,i)=>i);
 const timezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC';
 const dateTime=new Intl.DateTimeFormat('ru-RU',{timeZone:timezone,weekday:'short',day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'});
 const localDate=new Intl.DateTimeFormat('en-CA',{timeZone:timezone,year:'numeric',month:'2-digit',day:'2-digit'});
 const hourFormat=new Intl.DateTimeFormat('en-GB',{timeZone:timezone,hour:'2-digit',hourCycle:'h23'});
 const clock=(h:number)=>String(h%24).padStart(2,'0')+':00';
 const fmt=(value:string)=>dateTime.format(new Date(value));
 const percent=(busy:number,observed:number)=>observed?Math.round(busy/observed*100):null;
 let mode=$state<'plan'|'actual'>('plan');
 let data=$state<Overview|null>(null);
 let loading=$state(false);
 let error=$state('');
 let day=$state(0);
 let hour=$state(0);
 let grid=$state<HTMLDivElement>();
 const slot=$derived(data?.days[day]?.hours[hour]);
 const profileComplete=$derived(data&&!data.truncated&&!data.unmeasured_jobs&&!data.active_job);
 const activity=$derived(data?percent(data.busy_seconds,data.observed_seconds):null);

 async function load(){
  loading=true;error='';
  try{
   const params=new URLSearchParams({timezone});
   const response=await fetch('/api/v1/backup/workload?'+params,{credentials:'same-origin'});
   const body=await response.json();
   if(!response.ok)throw new Error(body.error??'Не удалось загрузить расписание');
   const first=data===null;
   data=body as Overview;
   if(first){
    const next=data.days[0].hours.findIndex(h=>!h.past);
    hour=Math.max(0,next);
   }
  }catch(err){
   error=err instanceof Error?err.message:'Не удалось загрузить расписание';
  }finally{
   loading=false;
  }
 }

 function slotKind(cell:Slot){
  if(mode==='plan'){
   if(cell.past)return 'past';
   if(cell.overlap)return 'conflict';
   if(cell.unknown)return 'unknown';
   return cell.planned?'planned':'free';
  }
  const value=percent(cell.busy_seconds,cell.observed_seconds);
  if(value===null)return 'past';
  if(value===0)return 'free';
  if(value<=33)return 'light';
  if(value<=66)return 'medium';
  return 'heavy';
 }

 function slotLabel(cell:Slot){
  if(mode==='actual'){
   const value=percent(cell.busy_seconds,cell.observed_seconds);
   return value===null?'Нет наблюдений':'Занято '+value+'%';
  }
  if(cell.past)return 'Прошедшее время';
  if(cell.overlap)return 'Пересечение';
  if(cell.unknown)return 'Нет оценки длительности';
  if(cell.planned)return 'Запланировано';
  return profileComplete?'Свободно по плану':'Нет известных запусков';
 }

 async function move(event:KeyboardEvent){
  const delta={ArrowLeft:-1,ArrowRight:1,ArrowUp:-24,ArrowDown:24}[event.key];
  if(delta===undefined)return;
  event.preventDefault();
  const index=Math.max(0,Math.min(167,day*24+hour+delta));
  day=Math.floor(index/24);hour=index%24;
  await tick();
  grid?.querySelector<HTMLButtonElement>('button[tabindex="0"]')?.focus();
 }

 async function selectFreeWindow(start:string){
  const time=new Date(start);
  const selected=data?.days.findIndex(item=>item.date===localDate.format(time))??-1;
  if(selected<0)return;
  day=selected;hour=Number(hourFormat.format(time));
  await tick();
  grid?.querySelector<HTMLButtonElement>('button[tabindex="0"]')?.focus();
 }

 onMount(()=>{void load();});
</script>

<section class="workload">
 <header class="workspace-header">
  <div><h1>Расписание и загрузка</h1><p class="secondary">Ближайшие семь календарных дней · {timezone}</p></div>
  <button class="outline" onclick={()=>void load()} disabled={loading}>Обновить</button>
 </header>
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 {#if data}
  <div class="workload-metrics">
   <section class="detail-card"><span>Плановых запусков</span><strong>{data.planned_runs}{data.truncated?'+':''}</strong></section>
   <section class="detail-card"><span>Часовые интервалы с пересечениями</span><strong>{data.overlap_hours}</strong></section>
   <section class="detail-card"><span>Доля времени работы исполнителя</span><strong>{activity===null?'—':activity+'%'}</strong></section>
  </div>
  <section class="detail-card">
   <div class="workload-toolbar">
    <div class="workload-switch" role="group" aria-label="Режим сетки">
     <button class:primary={mode==='plan'} class:outline={mode!=='plan'} aria-pressed={mode==='plan'} onclick={()=>mode='plan'}>План запусков</button>
     <button class:primary={mode==='actual'} class:outline={mode!=='actual'} aria-pressed={mode==='actual'} onclick={()=>mode='actual'}>Фактическая загрузка</button>
    </div>
   </div>
   {#if mode==='plan'}
    <p class="secondary">Длина окна — медиана завершённых запусков за последние {data.history_days} дней. При пересечении один исполнитель последовательно обработает задания.</p>
    {#if data.free_window}
     {@const window=data.free_window}
     <p class="window"><strong>Самое длинное свободное окно</strong> {fmt(window.start)} — {fmt(window.end)} <button class="text-button" onclick={()=>void selectFreeWindow(window.start)}>Показать в сетке</button></p>
    {/if}
    {#if data.unmeasured_jobs || data.active_job || data.truncated}
     <p class="secondary">
      {#if data.unmeasured_jobs}Без замера длительности: {data.unmeasured_jobs}. {/if}
      {#if data.active_job}Есть выполняющийся или поставленный в очередь запуск. {/if}
      {#if data.truncated}Достигнут предел числа отображаемых запусков. {/if}
      Свободные окна пока нельзя оценить полностью.
     </p>
    {/if}
   {:else}
    <p class="secondary">Журнал за последние {data.history_days} дней. Для каждого часа показана доля времени, когда исполнитель был занят. Время ожидания очереди, загрузка CPU и диска в этот показатель не входят.</p>
   {/if}
   <div class="workload-scroll" role="region" aria-label="Недельная сетка">
    <div class="workload-grid" bind:this={grid}>
     <span aria-hidden="true"></span>
     {#each hours as h (h)}<span class="hour-label">{String(h).padStart(2,'0')}</span>{/each}
     {#each data.days as item, i (item.date)}
      <strong class="day-label">{compact[item.weekday]} {mode==='plan'?item.date.slice(8):''}</strong>
      {#each item.hours as cell, h (h)}
       <button class="hour-cell {slotKind(cell)}" aria-label={weekdays[item.weekday]+' '+clock(h)+', '+slotLabel(cell)}
        aria-pressed={day===i&&hour===h} tabindex={day===i&&hour===h?0:-1} title={slotLabel(cell)}
        onclick={()=>{day=i;hour=h;}} onkeydown={(event)=>void move(event)}>
        {mode==='actual'?(percent(cell.busy_seconds,cell.observed_seconds)??'—'):cell.overlap?'×':cell.unknown?'?':cell.planned?cell.planned:''}
       </button>
      {/each}
     {/each}
    </div>
   </div>
   <div class="workload-legend">
    {#if mode==='plan'}
     <span><i class="free"></i>{profileComplete?'Свободно по плану':'Нет известных запусков'}</span><span><i class="planned"></i>Запланировано</span><span><i class="conflict"></i>Пересечение</span><span><i class="unknown"></i>Нет оценки</span>
    {:else}
     <span><i class="past"></i>Нет наблюдений</span><span><i class="free"></i>0%</span><span><i class="light"></i>1–33%</span><span><i class="medium"></i>34–66%</span><span><i class="heavy"></i>67–100%</span>
    {/if}
   </div>
  </section>
  {#if slot}
   <section class="detail-card workload-detail" aria-live="polite">
    <h2>{weekdays[data.days[day].weekday]} · {clock(hour)}–{clock(hour+1)}</h2>
    {#if mode==='plan'}
     <p>{slotLabel(slot)}</p>
     {#if slot.items?.length}
      <ul>{#each slot.items as item}
       <li>
        <strong>{item.job_name}</strong> · {fmt(item.start)}
        {#if item.end} — {fmt(item.end)} · {formatDuration(Date.parse(item.end)-Date.parse(item.start))}
        {:else} · длительность пока неизвестна{/if}
        {#if onEditJob}<button class="text-button" aria-label={'Редактировать задание «'+item.job_name+'»'} onclick={()=>onEditJob?.(item.job_id)}>Редактировать</button>{/if}
       </li>
      {/each}</ul>
      {#if slot.planned>slot.items.length}<p class="secondary">Ещё запусков: {slot.planned-slot.items.length}</p>{/if}
     {/if}
    {:else if slot.observed_seconds}
     <p><strong>{percent(slot.busy_seconds,slot.observed_seconds)}%</strong> · {slot.busy_seconds?formatDuration(slot.busy_seconds*1000):'0 с'} работы из {formatDuration(slot.observed_seconds*1000)} наблюдений</p>
    {:else}
     <p>Для этого часа ещё нет наблюдений.</p>
    {/if}
   </section>
  {/if}
 {:else if loading}
  <section class="detail-card" role="status">Загрузка расписания…</section>
 {/if}
</section>

<style>
 .workload{min-width:0}
 .workload-metrics{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}
 .workload-metrics .detail-card{display:grid;gap:8px}
 .workload-metrics span{font-size:13px;color:var(--muted);font-weight:650}
 .workload-metrics strong{font-size:27px;font-variant-numeric:tabular-nums}
 .workload-toolbar,.workload-switch{display:flex;gap:9px;flex-wrap:wrap}
 .window{font-size:13px;padding:11px 13px;border-left:4px solid #006246;background:#edf8f2}
 .window strong{margin-right:9px}
 .workload-scroll{overflow:auto;margin:17px 0;padding:3px}
 .workload-grid{min-width:820px;display:grid;grid-template-columns:43px repeat(24,minmax(25px,1fr));gap:4px;align-items:center}
 .hour-label,.day-label{text-align:center;color:#334864;font-size:11px;font-variant-numeric:tabular-nums}
 .hour-cell{height:29px;border:1px solid #8a9bb0;border-radius:4px;font-size:11px;font-weight:750;min-width:0;padding:0}
 .hour-cell[aria-pressed="true"]{outline:2px solid #112642;outline-offset:1px;z-index:1}
 .free{background:#fff;color:#223d60}
 .past{background:#e2e7ee;color:#45556c}
 .planned,.light{background:#8cb6ff;color:#102c59}
 .conflict{background:#a12232;color:#fff}
 .unknown{background:#8a4b04;color:#fff}
 .medium{background:#2459ab;color:#fff}
 .heavy{background:#142d59;color:#fff}
 .workload-legend{display:flex;flex-wrap:wrap;gap:14px;color:#334864;font-size:12px}
 .workload-legend span{display:flex;align-items:center;gap:6px}
 .workload-legend i{width:15px;height:15px;border-radius:3px;border:1px solid #889ab0}
 .workload-detail p,.workload-detail li{font-size:14px}
 .workload-detail li+li{margin-top:6px}
 @media(max-width:750px){.workload-metrics{grid-template-columns:1fr;gap:0}.workload-metrics .detail-card{margin-bottom:9px}.workload-metrics .detail-card:last-child{margin-bottom:18px}}
</style>
