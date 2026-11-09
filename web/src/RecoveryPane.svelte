<script lang="ts">
 import {onMount} from 'svelte';
 type Mode='password'|'no_password';
 type Mount={path:string;filesystem:string;free_bytes:number};
 type Folder={path:string;name:string};
 type ImportResult={storage:{name:string;path:string};snapshots:number;sources:number};
 let mounts=$state<Mount[]>([]);
 let entries=$state<Folder[]>([]);
 let chosen=$state('');
 let mode=$state<Mode>('no_password');
 let name=$state('');
 let keyText=$state('');
 let busy=$state(false);
 let error=$state('');
 let result=$state<ImportResult|null>(null);
 function csrf(){return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
 async function request<T>(path:string,opts:RequestInit={}):Promise<T>{
  const response=await fetch('/api/v1/backup'+path,{...opts,credentials:'same-origin'});
  const data:unknown=await response.json().catch(()=>null);
  if(!response.ok)throw new Error(data&&typeof data==='object'&&'error' in data?String((data as {error:unknown}).error):'HTTP '+response.status);
  return data as T;
 }
 async function load(){
  busy=true;error='';
  try{mounts=(await request<{mounts:Mount[]}>('/storages/mounts')).mounts}
  catch(e){error=e instanceof Error?e.message:'Не удалось получить список дисков'}
  finally{busy=false}
 }
 async function browse(path:string){
  busy=true;error='';
  try{
   entries=(await request<{entries:Folder[]}>('/storages/browse?path='+encodeURIComponent(path))).entries;
   chosen=path;
  }catch(e){error=e instanceof Error?e.message:'Каталог не удалось открыть'}
  finally{busy=false}
 }
 function back(){
  const base=mounts.filter(m=>chosen===m.path||chosen.startsWith(m.path+'/')).sort((a,b)=>b.path.length-a.path.length)[0];
  if(!base||chosen===base.path)return;
  const parent=chosen.slice(0,chosen.lastIndexOf('/'));
  void browse(parent.startsWith(base.path)?parent:base.path);
 }
 async function readKey(event:Event){
  const input=event.currentTarget as HTMLInputElement;
  const file=input.files?.[0];
  if(!file)return;
  if(file.size>512){error='Файл ключа слишком велик';return}
  keyText=await file.text();
 }
 async function importExisting(){
  busy=true;error='';result=null;
  try{
   result=await request<ImportResult>('/storages/import',{
    method:'POST',
    headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},
    body:JSON.stringify({name,parent_path:chosen,password_mode:mode,password:mode==='password'?keyText:''})
   });
   keyText='';name='';
  }catch(e){error=e instanceof Error?e.message:'Импорт завершился ошибкой'}
  finally{busy=false}
 }
 onMount(()=>{void load()});
</script>

<div class="heading"><div><span class="eyebrow">АВАРИЙНОЕ ВОССТАНОВЛЕНИЕ</span><h1>Восстановление после сбоя</h1><p class="secondary">Вернуть доступ к сохранённым данным после гибели прежнего сервера BackupHub.</p></div></div>
<section class="detail-card recovery-section">
 <h2>1. Сохранить комплект восстановления</h2>
 <p class="secondary">Скачайте Recovery Kit заранее и сохраните его отдельно от BackupHub. В нём содержатся согласованный каталог SQLite, ключ Vault и ключи парольных Restic-хранилищ. Сами большие архивы Restic в комплект не входят.</p>
 <p class="warning-recovery">Внимание: ZIP содержит секретные ключи и учётные данные в зашифрованном виде. Храните его в защищённом месте. Для полноценного восстановления конфигурации понадобятся сами диски с архивами.</p>
 <a class="recover-download" href="/api/v1/system/recovery-kit">Скачать Recovery Kit (.zip)</a>
</section>
<section class="detail-card recovery-section">
 <h2>2. Подключить архив с выжившего диска</h2>
 <p class="secondary">На новом сервере смонтируйте диск, затем укажите <strong>существующий каталог BackupHub</strong>, внутри которого находится <code>repository/config</code>. Импорт не форматирует диск, не создаёт репозиторий и не удаляет резервные копии.</p>
 <p class="secondary">Каталог должен принадлежать системному пользователю <code>backuphub</code> и иметь права <code>0700</code>. При отличающемся UID после переноса сначала исправьте доступ средствами администратора Linux.</p>
 {#if error}<p class="error" role="alert">{error}</p>{/if}
 {#if result}
  <p class="notice" role="status">Хранилище «{result.storage.name}» подключено. Восстановлено снимков: {result.snapshots}. Найдено архивных источников: {result.sources}. Данные доступны в разделе истории и для скачивания. Конфигурацию сетевых источников и расписание импорт маркеров не восстанавливает.</p>
 {/if}
 <div class="recover-mounts">
  {#each mounts as m (m.path)}
   <button class="outline" class:active={chosen===m.path} disabled={busy} onclick={()=>void browse(m.path)}>{m.path} ({m.filesystem})</button>
  {/each}
  <button class="outline" disabled={busy} onclick={()=>void load()}>Обновить диски</button>
 </div>
 {#if chosen}
  <div class="recover-browser">
   <strong>Папка: <code>{chosen}</code></strong>
   <button class="text-button" onclick={back} disabled={busy}>Выше</button>
   <div class="recover-folders">
    {#each entries as entry (entry.path)}
     <button class="outline" onclick={()=>void browse(entry.path)} disabled={busy}>{entry.name}/</button>
    {/each}
   </div>
  </div>
  <form class="recover-form" onsubmit={(event)=>{event.preventDefault();void importExisting()}}>
   <label>Название восстановленного хранилища<input required maxlength="120" placeholder="Архив с прежнего сервера" bind:value={name}/></label>
   <label>Режим архива<select bind:value={mode} disabled={busy}>
    <option value="no_password">Без пароля — только диск</option>
    <option value="password">С паролем — потребуется файл ключа</option>
   </select></label>
   {#if mode==='password'}
    <label>Ключ Restic (.key) <input type="file" accept=".key,text/plain" onchange={(e)=>void readKey(e)} disabled={busy}/></label>
    <label>Или вставьте содержимое ключа <input type="password" autocomplete="off" bind:value={keyText} disabled={busy} placeholder="Секрет из файла restic-...key"/></label>
   {/if}
   <button type="submit" disabled={busy||!chosen||!name.trim()||(mode==='password'&&!keyText.trim())}>{busy?'Проверка и импорт…':'Подключить и восстановить историю'}</button>
  </form>
 {/if}
</section>
<section class="detail-card recovery-section">
 <h2>3. Полностью вернуть прежнюю конфигурацию</h2>
 <p class="secondary">Восстановление полного Recovery Kit пока выполняется только через безопасную офлайн-команду <code>backuphub recovery-kit-restore</code> в <strong>новый пустой каталог</strong> при остановленной службе. Перезапись действующей SQLite через веб-интерфейс запрещена. После восстановления необходимо вернуть прежние пути до оригинальных архивов или восстановить их отдельно.</p>
</section>
<style>
 .recovery-section{display:grid;gap:14px;margin-bottom:18px}
 .recovery-section h2{margin:0}.recovery-section p{margin:0}
 .warning-recovery{padding:12px;border:1px solid #b98850;border-radius:7px}
 .recover-download{display:inline-block;justify-self:start;padding:11px 15px;background:#2e5484;border-radius:7px;color:#fff;text-decoration:none;font-weight:600}
 .recover-mounts,.recover-folders{display:flex;gap:8px;flex-wrap:wrap}
 .recover-browser{display:grid;gap:12px;border:1px solid #405474;border-radius:8px;padding:12px}
 .recover-form{display:grid;gap:14px;max-width:720px}
 .recover-form label{display:grid;gap:6px}
 .recover-form input,.recover-form select{width:100%;padding:10px;border-radius:7px;background:#172338;border:1px solid #405474;color:#eee}
 .recover-form button{justify-self:start}
</style>
