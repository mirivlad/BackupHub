<script lang="ts">
 import {onMount} from 'svelte';

 type Journal={text:string;truncated:boolean};
 let journal=$state<Journal|null>(null);
 let loading=$state(false);
 let error=$state('');
 const controller=new AbortController();

 async function refresh(){
  if(loading)return;
  loading=true;
  try{
   const response=await fetch('/api/v1/system/journal',{credentials:'same-origin',signal:controller.signal});
   const body=await response.json();
   if(response.status===401||response.status===403)journal=null;
   if(!response.ok)throw new Error(body.error||'Журнал службы недоступен');
   journal=body as Journal;
   error='';
  }catch(err){
   if(!controller.signal.aborted)error=err instanceof Error?err.message:'Журнал службы недоступен';
  }finally{
   loading=false;
  }
 }

 onMount(()=>{
  void refresh();
  const poll=setInterval(()=>void refresh(),4000);
  return ()=>{clearInterval(poll);controller.abort();};
 });
</script>

<section>
 <header class="workspace-header">
  <div><h1>Журнал службы</h1><p class="secondary">Последние 100 записей systemd для BackupHub. Новые записи появляются сверху.</p></div>
  <button class="outline" onclick={()=>void refresh()} disabled={loading}>Обновить</button>
 </header>
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 <section class="detail-card">
  {#if journal?.text}
   <!-- svelte-ignore a11y_no_noninteractive_tabindex (Allow keyboard scrolling of the journal.) -->
   <div class="journal-text" role="region" tabindex="0" aria-label="Записи журнала BackupHub"><pre>{journal.text}</pre></div>
   {#if journal.truncated}<p class="secondary">Объём вывода ограничен. Показаны самые свежие записи.</p>{/if}
  {:else if !error}
   <p class="secondary" role="status">{journal?'В журнале службы нет доступных записей.':'Загрузка журнала…'}</p>
  {/if}
 </section>
</section>

<style>
 .journal-text{padding:16px;max-height:65vh;overflow:auto;border-radius:8px;background:#121e32;color:#edf3ff}
 pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.65 ui-monospace,monospace}
</style>
