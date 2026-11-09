<script lang="ts">
  import { onMount } from 'svelte';

  type Role = string;
  type Source = { id:string; name:string; kind:string; host:string; share:string; path:string; username:string; database:string; interval_minutes:number; enabled:boolean; next_run:string };
  type Snapshot = { id:string; source_id:string; source_name:string; kind:string; created_at:string; verification:string; fidelity:string; consistency:string };
  type Run = { id:string; task_id:string; state:string; started_at:string; finished_at?:string; failure_code?:string };
  type File = { path:string; name:string; type:string; size:number; modified:string };
  let { role }: {role:Role} = $props();
  let sources = $state<Source[]>([]);
  let snapshots = $state<Snapshot[]>([]);
  let runs = $state<Run[]>([]);
  let files = $state<File[]>([]);
  let activeSnapshot = $state<Snapshot|null>(null);
  let selection = $state<string[]>([]);
  let filter = $state('');
  let busy = $state(false);
  let message = $state('');
  let error = $state('');
  let editor = $state(false);
  let form = $state({name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:'',interval_minutes:1440});
  const canConfigure = $derived(role === 'administrator');
  const canRun = $derived(role === 'administrator' || role === 'backup_operator');
  const canExport = $derived(role === 'administrator' || role === 'restore_operator');
  let visibleFiles = $derived(files.filter(file=>!filter || file.path.toLocaleLowerCase('ru-RU').includes(filter.toLocaleLowerCase('ru-RU'))));
  function datetime(value: string): string {const date = new Date(value);return Number.isNaN(date.getTime())?value:date.toLocaleString('ru-RU');}
  function csrf():string{return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
  async function api<T>(path:string,init:RequestInit={}):Promise<T>{
    const response=await fetch('/api/v1/backup'+path,{credentials:'same-origin',...init});
    const json:unknown=await response.json().catch(()=>null);
    if(!response.ok){const msg=json&&typeof json==='object'&&'error' in json?String((json as {error:unknown}).error):`HTTP ${response.status}`;throw new Error(msg);}
    return json as T;
  }
  function jsonBody(value:unknown):RequestInit{return {method:'POST',headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},body:JSON.stringify(value)};}
  function postNoBody():RequestInit{return {method:'POST',headers:{'X-BackupHub-CSRF':csrf()}};}
  async function refresh(){
    busy=true;error='';
    try{
      const [a,b,c]=await Promise.all([api<{sources:Source[]}>('/sources'),api<{snapshots:Snapshot[]}>('/snapshots'),api<{runs:Run[]}>('/runs')]);
      sources=a.sources;snapshots=b.snapshots;runs=c.runs;
    }catch(e){error=e instanceof Error?e.message:'Ошибка загрузки';}
    finally{busy=false;}
  }
  async function create(){
    busy=true;error='';message='';
    try{
      const result=await api<Source>('/sources',jsonBody(form));
      message=`Источник «${result.name}» создан. Копирование по расписанию начнётся после наступления времени задания.`;
      form={name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:'',interval_minutes:1440};
      editor=false;await refresh();
    }catch(e){error=e instanceof Error?e.message:'Не удалось добавить источник';}
    finally{busy=false;}
  }
  async function act(id:string,action:'run'|'pause'|'resume'){
    busy=true;error='';message='';
    try{await api(`/sources/${encodeURIComponent(id)}/${action}`,postNoBody());message=action==='run'?'Задание поставлено в очередь. Проверьте журнал запусков.':'Расписание обновлено.';await refresh();}
    catch(e){error=e instanceof Error?e.message:'Операция не выполнена';}finally{busy=false;}
  }
  async function openSnapshot(snap:Snapshot){
    busy=true;error='';message='';
    try{const data=await api<{files:File[]}>(`/snapshots/${encodeURIComponent(snap.id)}/files`);activeSnapshot=snap;files=data.files;selection=[];filter='';}
    catch(e){error=e instanceof Error?e.message:'Невозможно открыть снимок';}finally{busy=false;}
  }
  function selected(path:string){selection=selection.includes(path)?selection.filter(x=>x!==path):[...selection,path];}
  function download(paths:string[],single=false){
    if(!activeSnapshot)return;
    const qs=new URLSearchParams();for(const path of paths){qs.append('path',path)}
    if(single){qs.set('mode','single')}
    window.location.assign(`/api/v1/backup/snapshots/${encodeURIComponent(activeSnapshot.id)}/download?${qs.toString()}`);
  }
  async function restore(){
    if(!activeSnapshot)return;
    if(!window.confirm('Восстановить выбранные файлы в новый отдельный каталог на сервере? Существующие файлы не изменяются.'))return;
    busy=true;error='';message='';
    try{
      const result=await api<{directory:string}>(`/snapshots/${activeSnapshot.id}/restore`,jsonBody({paths:selection}));
      message=`Данные восстановлены в отдельный каталог сервера: ${result.directory}`;
    }catch(e){error=e instanceof Error?e.message:'Ошибка восстановления';}finally{busy=false;}
  }
  onMount(()=>{void refresh();});
</script>

<section class="backup-workspace">
  <div class="heading"><div><span class="eyebrow">РЕЗЕРВНОЕ КОПИРОВАНИЕ</span><h1>Источники и история</h1><p class="secondary">Файловые снимки Restic, история версий и восстановление из конкретной даты.</p></div><button class="outline" onclick={()=>void refresh()} disabled={busy}>Обновить</button></div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if message}<p class="notice" role="status">{message}</p>{/if}
  <div class="warning"><strong>Предварительная версия.</strong><span>Копирование SMB/SFTP — без гарантии ACL и согласованности открытых файлов. PostgreSQL сохраняется как логический дамп. Нативный профиль PostgreSQL Pro 1C и AD пока не поддерживаются.</span></div>
  <section class="detail-card">
    <div class="backup-heading"><h2>Источники</h2>{#if canConfigure}<button class="outline" onclick={()=>editor=!editor}>{editor?'Закрыть':'Добавить источник'}</button>{/if}</div>
    {#if editor && canConfigure}
      <form class="backup-form" onsubmit={(event)=>{event.preventDefault();void create();}}>
        <label>Название <input bind:value={form.name} maxlength="120" required placeholder="Общая папка отдела" /></label>
        <label>Тип <select bind:value={form.kind}><option value="smb">SMB2/3 — сетевая шара</option><option value="sftp">Linux SSH/SFTP</option><option value="local">Локальный каталог</option><option value="postgres">PostgreSQL — обычная БД</option></select></label>
        {#if form.kind !== 'local'}
          <label>Сервер <input bind:value={form.host} required placeholder={form.kind==='smb'?'fileserver.example.com':form.kind==='sftp'?'linux.example.com:22':'postgres.example.com:5432'} /></label>
          <label>Пользователь <input bind:value={form.username} required autocomplete="off" /></label>
          <label>Пароль <input type="password" bind:value={form.password} required autocomplete="new-password" /></label>
        {/if}
        {#if form.kind==='smb'}
          <label>Шара <input bind:value={form.share} required placeholder="Shared" /></label>
          <label>Подкаталог (необязательно) <input bind:value={form.path} placeholder="Department/Video" /></label>
        {:else if form.kind==='sftp'}
          <label>Путь на сервере <input bind:value={form.path} required placeholder="/srv/data" /></label>
          <p class="secondary">Для SFTP проверка SSH host key обязательна: добавьте ключ сервера в /etc/backuphub/known_hosts.</p>
        {:else if form.kind==='local'}
          <label>Абсолютный путь на BackupHub <input bind:value={form.path} required placeholder="/srv/data" /></label>
        {:else}
          <label>Название базы <input bind:value={form.database} required placeholder="production" /></label>
          <p class="secondary">Только обычный pg_dump. Для PostgreSQL Pro 1C с большой public.config нужен отдельный профиль (ещё не готов).</p>
        {/if}
        <label>Интервал (минуты) <input type="number" bind:value={form.interval_minutes} min="5" max="525600" required /></label>
        <button type="submit" disabled={busy}>Сохранить источник</button>
      </form>
    {/if}
    {#if sources.length===0}<p class="secondary">Источников пока нет. Добавьте первый, чтобы создать расписание.</p>{:else}
      <div class="table-scroll"><table><thead><tr><th>Источник</th><th>Тип</th><th>Период</th><th>Следующее выполнение</th><th>Действия</th></tr></thead><tbody>
      {#each sources as source (source.id)}<tr><td><strong>{source.name}</strong><small class="source-location">{source.kind==='smb'?`//${source.host}/${source.share}/${source.path}`:source.kind==='postgres'?`${source.host}/${source.database}`:source.path}</small></td><td>{source.kind}</td><td>{source.interval_minutes} мин.</td><td>{source.enabled?datetime(source.next_run):'Приостановлено'}</td><td class="action-cell">{#if canRun}<button class="outline" onclick={()=>void act(source.id,'run')} disabled={busy}>Запустить</button>{/if}{#if canConfigure}<button class="text-button" onclick={()=>void act(source.id,source.enabled?'pause':'resume')} disabled={busy}>{source.enabled?'Пауза':'Включить'}</button>{/if}</td></tr>{/each}
      </tbody></table></div>
    {/if}
  </section>
  <section class="detail-card">
    <h2>История резервных копий</h2>
    {#if snapshots.length===0}<p class="secondary">Пока нет завершённых снимков. Убедитесь, что источник доступен, и выполните задание.</p>{:else}
      <div class="table-scroll"><table><thead><tr><th>Дата</th><th>Источник</th><th>Контроль данных</th><th>Действие</th></tr></thead><tbody>
      {#each snapshots as snapshot (snapshot.id)}<tr><td>{datetime(snapshot.created_at)}</td><td>{snapshot.source_name}</td><td>{snapshot.verification}<small class="source-location">{snapshot.consistency} · {snapshot.fidelity}</small></td><td><button class="outline" onclick={()=>void openSnapshot(snapshot)} disabled={busy}>Открыть снимок</button></td></tr>{/each}
      </tbody></table></div>
    {/if}
  </section>
  {#if activeSnapshot}
    <section class="detail-card">
      <div class="backup-heading"><div><h2>Файлы · {activeSnapshot.source_name}</h2><p class="secondary">Снимок от {datetime(activeSnapshot.created_at)}. Выделите файлы или каталоги для выгрузки или восстановления.</p></div><button class="text-button" onclick={()=>{activeSnapshot=null;files=[];selection=[];}}>Закрыть</button></div>
      <label>Поиск по имени и пути <input class="backup-search" bind:value={filter} placeholder="Video, отчет.xlsx" /></label>
      <div class="backup-actions">{#if canExport}<button onclick={()=>download(selection)} disabled={busy||selection.length===0}>Скачать выбранное ZIP ({selection.length})</button><button class="outline" onclick={()=>download([])} disabled={busy}>Скачать весь снимок ZIP</button><button class="outline" onclick={()=>void restore()} disabled={busy}>Восстановить в новую папку на сервере</button>{/if}</div>
      <div class="table-scroll backup-file-list"><table><thead><tr><th>Выбор</th><th>Путь</th><th>Тип</th><th>Размер</th><th>Дата изменения</th><th>Файл</th></tr></thead><tbody>
      {#each visibleFiles.slice(0,2000) as file (file.path)}<tr><td><input type="checkbox" checked={selection.includes(file.path)} onchange={()=>selected(file.path)} disabled={!canExport||file.type==='other'} /></td><td class="path-cell">{file.path}</td><td>{file.type==='dir'?'Каталог':file.type==='file'?'Файл':'Спец. объект'}</td><td>{file.type==='file'?new Intl.NumberFormat('ru-RU').format(file.size):'—'}</td><td>{datetime(file.modified)}</td><td>{#if canExport&&file.type==='file'}<button class="text-button" onclick={()=>download([file.path],true)}>Скачать</button>{/if}</td></tr>{/each}
      </tbody></table></div>
      {#if visibleFiles.length>2000}<p class="secondary">Показаны первые 2000 записей. Уточните поиск.</p>{/if}
    </section>
  {/if}
  <section class="detail-card">
    <h2>Журнал выполнения</h2>
    {#if runs.length===0}<p class="secondary">Запусков пока нет.</p>{:else}<div class="table-scroll"><table><thead><tr><th>Начало</th><th>Задание</th><th>Состояние</th><th>Код результата</th></tr></thead><tbody>
      {#each runs as run (run.id)}<tr><td>{datetime(run.started_at)}</td><td><code>{run.task_id.slice(0,8)}</code></td><td>{run.state}</td><td>{run.failure_code??'—'}</td></tr>{/each}
    </tbody></table></div>{/if}
  </section>
</section>

<style>
 .backup-workspace{display:grid;gap:18px}.backup-heading{display:flex;justify-content:space-between;align-items:center;gap:20px;margin-bottom:14px}.backup-form{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px;padding:16px 0}.backup-form label{display:grid;gap:6px}.backup-form input,.backup-form select,.backup-search{width:100%;max-width:100%;min-width:0;padding:10px;border:1px solid #405474;border-radius:7px;background:#172338;color:#eee}.backup-form p{grid-column:1/-1}.backup-form button{align-self:end}.backup-actions{display:flex;flex-wrap:wrap;gap:10px;margin:16px 0}.source-location{display:block;opacity:.7;font-size:12px;margin-top:4px}.path-cell{max-width:480px;overflow-wrap:anywhere}.action-cell{white-space:nowrap}.backup-file-list{max-height:520px;overflow:auto}.backup-heading h2{margin:0 0 5px}
</style>
