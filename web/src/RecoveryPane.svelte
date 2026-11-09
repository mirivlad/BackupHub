<script lang="ts">
 import {onMount} from 'svelte';
 type Mode='password'|'no_password';
 type Mount={path:string;filesystem:string;free_bytes:number};
 type Folder={path:string;name:string};
 type ImportResult={storage:{name:string;path:string};snapshots:number;sources:number};
 type Storage={id:string;name:string;path:string;status:string;password_mode:Mode};
 let mounts=$state<Mount[]>([]);
 let storages=$state<Storage[]>([]);
 let kitFile=$state<File|null>(null);
 let confirmReplace=$state(false);
 let kitNotice=$state('');
 let rebindId=$state('');
 let rebindNotice=$state('');
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
  try{
   const [a,b]=await Promise.all([request<{mounts:Mount[]}>('/storages/mounts'),request<{storages:Storage[]}>('/storages')]);
   mounts=a.mounts;storages=b.storages;
  }
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
 async function uploadKit(){
  if(!kitFile||!confirmReplace){error='Подтверди замену пустой установки';return;}
  busy=true;error='';kitNotice='';
  try{
   const form=new FormData();
   form.append('kit',kitFile,kitFile.name);
   const response=await fetch('/api/v1/system/recovery-kit/import',{
    method:'POST',credentials:'same-origin',
    headers:{'X-BackupHub-CSRF':csrf(),'X-BackupHub-Recovery-Confirm':'REPLACE_EMPTY_INSTALL'},
    body:form
   });
   const data=await response.json();
   if(!response.ok)throw new Error(data.error??'Не удалось проверить Recovery Kit');
   kitNotice='Комплект проверен. '+(data.restart_requested
    ?'BackupHub автоматически перезапускается, конфигурация и ключи прежней установки восстанавливаются. Затем обнови страницу и войди со СТАРЫМИ учётными данными.'
    :'Восстановление подготовлено. Службу нужно штатно перезапустить, чтобы применить комплект.');
   kitFile=null;confirmReplace=false;
  }catch(e){error=e instanceof Error?e.message:'Ошибка загрузки'}
  finally{busy=false;}
 }
 async function rebindStorage(){
  if(!chosen||!rebindId)return;
  busy=true;error='';rebindNotice='';
  try{
   const updated=await request<Storage>('/storages/'+encodeURIComponent(rebindId)+'/rebind',{
    method:'POST',headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},
    body:JSON.stringify({parent_path:chosen})
   });
   rebindNotice='Архив «'+updated.name+'» переподключён: '+updated.path+'. Старые снимки доступны в истории.';
   await load();
  }catch(e){error=e instanceof Error?e.message:'Не удалось переподключить архив'}
  finally{busy=false}
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
 <h2>2. Загрузить сохранённый комплект на новый BackupHub</h2>
 <p class="secondary">На новом сервере установи BackupHub, создай временного администратора, войди и загрузи ранее скачанный Recovery Kit. После проверки система вернёт прежние настройки и ключи. <strong>Это разрешено только на новой установке без заданий, источников и снимков.</strong></p>
 <p class="warning-recovery">Временный администратор нового сервера будет заменён прежними пользователями из комплекта. После автоматического перезапуска войди с прежним логином и паролем. Пустая новая установка сохраняется отдельно для отката.</p>
 <form class="recover-form" onsubmit={(event)=>{event.preventDefault();void uploadKit()}}>
  <label>Архив с настройками и ключами (.zip)
   <input type="file" accept=".zip,application/zip" onchange={(event)=>{kitFile=(event.currentTarget as HTMLInputElement).files?.[0]??null}} disabled={busy}/>
  </label>
  <label class="recover-check"><input type="checkbox" bind:checked={confirmReplace}/> Подтверждаю восстановление старой конфигурации вместо пустой установки</label>
  <button type="submit" disabled={busy||!kitFile||!confirmReplace}>{busy?'Проверка комплекта…':'Загрузить и восстановить конфигурацию'}</button>
 </form>
 {#if kitNotice}<p class="notice" role="status">{kitNotice}</p>{/if}
</section>
<section class="detail-card recovery-section">
 <h2>3. Подключить старый диск с резервными копиями</h2>
 <p class="secondary">На новом сервере смонтируй старый диск, затем выбери <strong>существующую папку BackupHub</strong>, внутри которой находится <code>repository/config</code>. Программа проверит данные без форматирования диска.</p>
 <p class="secondary">Если был загружен Recovery Kit, прежние хранилища уже перечислены в настройках и могут быть переподключены по новым путям. Если комплекта нет, можно восстановить историю напрямую с диска отдельным импортом.</p>
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
  {#if storages.length>0}
   <form class="recover-form" onsubmit={(event)=>{event.preventDefault();void rebindStorage()}}>
    <h3>Переподключить хранилище из восстановленных настроек</h3>
    <label>Существующее хранилище
     <select bind:value={rebindId} disabled={busy}>
      <option value="">Выбери прежнее хранилище</option>
      {#each storages as storage (storage.id)}
       <option value={storage.id}>{storage.name} · {storage.status==='available'?'доступно':'не найдено'} · {storage.path}</option>
      {/each}
     </select>
    </label>
    <p class="secondary">Будут проверены все записанные снимки. Пароли и ключи берутся из Recovery Kit, повторно вводить их не нужно.</p>
    <button type="submit" disabled={busy||!chosen||!rebindId}>Проверить и переподключить прежнее хранилище</button>
   </form>
   {#if rebindNotice}<p class="notice" role="status">{rebindNotice}</p>{/if}
  {/if}
  <form class="recover-form" onsubmit={(event)=>{event.preventDefault();void importExisting()}}>
   <h3>Импортировать историю только с диска (без Recovery Kit)</h3>
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
 <h2>4. Офлайн-восстановление для нестандартных случаев</h2>
 <p class="secondary">В стандартной DEB-установке загрузки ZIP через браузер достаточно: подтверждённый комплект применяется автоматически при штатном перезапуске службы. Для сложных случаев сохранена команда <code>backuphub recovery-kit-restore</code>, которая разворачивает комплект только в новый пустой каталог, без перезаписи существующей SQLite.</p>
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
 .recover-check{display:flex!important;align-items:center;gap:9px}
 .recover-check input{width:auto!important}
 .recover-form h3{margin:2px 0 0}
</style>
