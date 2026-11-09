<script lang="ts">
 import {onMount} from 'svelte';
 type Storage={id:string;name:string;path:string;free_bytes:number;status:string};
 type Mount={path:string;filesystem:string;free_bytes:number};
 type Entry={name:string;path:string};
 let {onCreated}:{onCreated:()=>Promise<void>}=$props();
 let storages=$state<Storage[]>([]);
 let mounts=$state<Mount[]>([]);
 let entries=$state<Entry[]>([]);
 let chosen=$state('');
 let selectedMount=$state('');
 let storageName=$state('');
 let folder=$state('BackupHub');
 let busy=$state(false);
 let error=$state('');
 let notice=$state('');
 const gb=(bytes:number)=>new Intl.NumberFormat('ru-RU',{maximumFractionDigits:1}).format(bytes/1073741824)+' ГиБ';
 function csrf(){return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
 async function request<T>(path:string,opts:RequestInit={}):Promise<T>{
  const response=await fetch('/api/v1/backup'+path,{...opts,credentials:'same-origin'});
  const data:unknown=await response.json().catch(()=>null);
  if(!response.ok)throw new Error(data&&typeof data==='object'&&'error' in data?String((data as {error:unknown}).error):'HTTP '+response.status);
  return data as T;
 }
 async function load(){
  busy=true;error='';
  try{
   const [a,b]=await Promise.all([request<{storages:Storage[]}>('/storages'),request<{mounts:Mount[]}>('/storages/mounts')]);
   storages=a.storages;mounts=b.mounts;
  }catch(e){error=e instanceof Error?e.message:'Список дисков недоступен';}
  finally{busy=false;}
 }
 async function browse(p:string){
  busy=true;error='';
  try{
   const data=await request<{entries:Entry[]}>('/storages/browse?path='+encodeURIComponent(p));
   chosen=p;entries=data.entries;
   if(mounts.some(x=>x.path===p))selectedMount=p;
  }catch(e){error=e instanceof Error?e.message:'Невозможно открыть папку';}
  finally{busy=false;}
 }
 async function create(){
  busy=true;error='';notice='';
  try{
   const created=await request<Storage>('/storages',{method:'POST',headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},body:JSON.stringify({name:storageName,parent_path:chosen,folder})});
   notice='Хранилище «'+created.name+'» подключено: '+created.path;
   storageName='';folder='BackupHub';
   await load();await onCreated();
  }catch(e){error=e instanceof Error?e.message:'Не удалось создать хранилище';}
  finally{busy=false;}
 }
 function back(){
  const m=mounts.find(x=>chosen===x.path||chosen.startsWith(x.path+'/'));
  if(!m)return;
  if(chosen===m.path)return;
  const p=chosen.slice(0,chosen.lastIndexOf('/'));
  void browse(p.startsWith(m.path)?p:m.path);
 }
 onMount(()=>{void load();});
</script>
<section class="detail-card">
 <div class="storage-heading"><div><h2>Хранилища резервных копий</h2><p class="secondary">Каждый путь — отдельный зашифрованный Restic-репозиторий. У каждого задания своё хранилище.</p></div><button class="outline" onclick={()=>void load()} disabled={busy}>Обновить диски</button></div>
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 {#if notice}<p class="notice" role="status">{notice}</p>{/if}
 <div class="storage-grid">
  {#each storages as s (s.id)}
   <div class="storage-item"><strong>{s.name}</strong><code>{s.path}</code><small>{s.status==='available'?'Доступно · свободно '+gb(s.free_bytes):'Недоступно — задания будут завершаться ошибкой'}</small>{#if s.id==='primary'}<small>Встроенное хранилище для совместимости</small>{/if}</div>
  {/each}
 </div>
 <h3>Добавить новое хранилище</h3>
 <p class="secondary">Выбери смонтированный том, открой нужную папку и создай там отдельный каталог BackupHub. Системные каталоги скрыты. У уже существующих данных права не меняются.</p>
 {#if mounts.length===0}
  <p class="secondary">Подходящих смонтированных томов нет. Подключи диск или сетевое хранилище в /mnt, /media, /backup, /backup2 либо /srv/backups.</p>
 {:else}
  <div class="mount-list">
   {#each mounts as m (m.path)}
    <button class="outline" class:active={chosen===m.path} disabled={busy} onclick={()=>void browse(m.path)}>{m.path} · {m.filesystem} · {gb(m.free_bytes)} свободно</button>
   {/each}
  </div>
  {#if chosen}
   <div class="storage-browser">
    <div class="storage-heading"><strong>Выбранная папка: <code>{chosen}</code></strong><button class="text-button" disabled={busy||chosen===selectedMount} onclick={back}>На уровень выше</button></div>
    <div class="storage-folders">
     {#each entries as entry (entry.path)}
      <button class="outline" disabled={busy} onclick={()=>void browse(entry.path)}>📁 {entry.name}</button>
     {/each}
     {#if entries.length===0}<p class="secondary">Нет вложенных каталогов.</p>{/if}
    </div>
    <form class="storage-form" onsubmit={(event)=>{event.preventDefault();void create();}}>
     <label>Название хранилища<input required maxlength="120" bind:value={storageName} placeholder="Архив на диске 1"/></label>
     <label>Новая папка<input required minlength="1" maxlength="64" bind:value={folder}/></label>
     <button type="submit" disabled={busy||!chosen}>Создать папку и подключить хранилище</button>
    </form>
   </div>
  {/if}
 {/if}
</section>
<style>
 .storage-heading{display:flex;justify-content:space-between;align-items:center;gap:14px;flex-wrap:wrap;margin-bottom:12px}
 .storage-heading h2{margin:0 0 6px}.storage-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:12px;margin-bottom:20px}
 .storage-item{display:grid;gap:7px;padding:13px;border:1px solid #405474;border-radius:9px}.storage-item code{overflow-wrap:anywhere}.storage-item small{opacity:.75}
 .mount-list{display:flex;gap:9px;flex-wrap:wrap}.mount-list button{white-space:normal;text-align:left}
 .storage-browser{margin-top:14px;border:1px solid #405474;border-radius:9px;padding:14px}.storage-folders{display:flex;gap:8px;flex-wrap:wrap;max-height:210px;overflow:auto;margin-bottom:14px}
 .storage-form{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;align-items:end}
 .storage-form label{display:grid;gap:6px}.storage-form input{background:#172338;color:#eee;border:1px solid #405474;border-radius:7px;padding:10px;min-width:0;width:100%}
</style>
