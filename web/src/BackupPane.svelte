<script lang="ts">
 import {onMount} from 'svelte';
 import StoragePane from './StoragePane.svelte';
 type Section='overview'|'jobs'|'sources'|'storages'|'history'|'runs';
 type Source={id:string;name:string;kind:string;host:string;share:string;path:string;username:string;database:string;};
 type Storage={id:string;name:string;path:string;free_bytes:number;status:string;password_mode:string};
 type Profile={mode:'interval'|'daily'|'weekly';interval_value:number;interval_unit:string;days_of_week:string;hour:number;minute:number;timezone:string;first_run:string};
 type Job={id:string;name:string;source_id:string;source_name:string;repository_id:string;schedule:Profile;enabled:boolean;next_run:string;queued?:boolean};
 type Snapshot={id:string;source_id:string;source_name:string;kind:string;created_at:string;verification:string;fidelity:string;consistency:string};
 type Run={id:string;task_id:string;state:string;started_at:string;finished_at?:string;failure_code?:string};
 type File={path:string;name:string;type:string;size:number;modified:string};
 let {section,role,onNavigate}:{section:Section;role:string;onNavigate:(section:Section)=>void}=$props();
 let sources=$state<Source[]>([]);
 let storages=$state<Storage[]>([]);
 let jobs=$state<Job[]>([]);
 let snapshots=$state<Snapshot[]>([]);
 let runs=$state<Run[]>([]);
 let files=$state<File[]>([]);
 let activeSnapshot=$state<Snapshot|null>(null);
 let selection=$state<string[]>([]);
 let filter=$state('');
 let busy=$state(false);
 let loaded=$state(false);
 let error=$state('');
 let notice=$state('');
 let editing=$state(false);
 let sourceForm=$state({name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:''});
 let jobForm=$state({name:'',source_id:'',repository_id:''});
 let scheduleForm=$state({mode:'daily' as 'daily'|'weekly'|'interval',time:'02:00',startDate:'',timezone:'UTC',interval:7,unit:'days',weekdays:[1,2,3,4,5] as number[]});
 const weekdays=[{id:1,label:'Пн'},{id:2,label:'Вт'},{id:3,label:'Ср'},{id:4,label:'Чт'},{id:5,label:'Пт'},{id:6,label:'Сб'},{id:0,label:'Вс'}];
 const canConfigure=$derived(role==='administrator');
 const canRun=$derived(role==='administrator'||role==='backup_operator');
 const canExport=$derived(role==='administrator'||role==='restore_operator');
 const visibleFiles=$derived(files.filter(f=>!filter||f.path.toLocaleLowerCase('ru-RU').includes(filter.toLocaleLowerCase('ru-RU'))));
 const availableStorages=$derived(storages.filter(s=>s.status==='available'));
 const activeJobs=$derived(jobs.filter(j=>j.enabled));
 const failedRuns=$derived(runs.filter(r=>r.state==='failed'||r.state==='interrupted'));
 const fmt=(v:string)=>{if(!v)return '—';const d=new Date(v);return Number.isNaN(d.getTime())?'—':d.toLocaleString('ru-RU',{dateStyle:'short',timeStyle:'short'});};
 const size=(bytes:number)=>new Intl.NumberFormat('ru-RU',{maximumFractionDigits:1}).format(bytes/1073741824)+' ГиБ';
 function sourceType(kind:string){return ({smb:'SMB',sftp:'SFTP',postgres:'PostgreSQL',local:'Локальный каталог'} as Record<string,string>)[kind]??kind;}
 function sourcePath(s:Source){return s.kind==='smb'?'//'+s.host+'/'+s.share+(s.path?'/'+s.path:''):s.kind==='postgres'?s.host+'/'+s.database:s.kind==='sftp'?s.host+':'+s.path:s.path;}
 function stateLabel(state:string){return ({completed:'Завершено',completed_with_warnings:'С предупреждениями',failed:'Ошибка',queued:'В очереди',preflight:'Проверка',preparing:'Подготовка',capturing:'Копирование',committing:'Сохранение',verifying:'Проверка данных',waiting_for_source:'Ожидание источника',interrupted:'Прервано',cancelled:'Отменено'} as Record<string,string>)[state]??state;}
 function scheduleText(p:Profile){
  if(!p)return '—';
  const at=String(p.hour).padStart(2,'0')+':'+String(p.minute).padStart(2,'0');
  if(p.mode==='daily')return 'Каждый день в '+at+' · '+p.timezone;
  if(p.mode==='weekly')return p.days_of_week.split(',').map(n=>weekdays.find(x=>x.id===Number(n))?.label??n).join(', ')+' в '+at+' · '+p.timezone;
  const units:Record<string,string>={minutes:'мин.',hours:'ч.',days:'дн.'};
  return 'Каждые '+p.interval_value+' '+(units[p.interval_unit]??p.interval_unit)+' с '+p.first_run.replace('T',' ')+' · '+p.timezone;
 }
 function csrf(){return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
 async function api<T>(path:string,opts:RequestInit={}):Promise<T>{const r=await fetch('/api/v1/backup'+path,{credentials:'same-origin',...opts});const body=await r.json().catch(()=>null);
  if(!r.ok)throw new Error(body?.error??'Ошибка запроса: HTTP '+r.status);return body as T;}
 function jsonBody(value:unknown):RequestInit{return {method:'POST',headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},body:JSON.stringify(value)};}
 function actionBody():RequestInit{return {method:'POST',headers:{'X-BackupHub-CSRF':csrf()}};}
 async function refresh(){busy=true;error='';
  try{const [a,b,c,d,e]=await Promise.all([
   api<{sources:Source[]}>('/sources'),api<{storages:Storage[]}>('/storages'),api<{jobs:Job[]}>('/jobs'),
   api<{snapshots:Snapshot[]}>('/snapshots'),api<{runs:Run[]}>('/runs')]);
   sources=a.sources;storages=b.storages;jobs=c.jobs;snapshots=d.snapshots;runs=e.runs;loaded=true;
  }catch(e){error=e instanceof Error?e.message:'Данные недоступны';}
  finally{busy=false;}
 }
 async function runAction(fn:()=>Promise<void>){busy=true;error='';notice='';
  try{await fn();editing=false;await refresh();}catch(e){error=e instanceof Error?e.message:'Операция не выполнена';}
  finally{busy=false;}
 }
 async function createSource(){await runAction(async()=>{const v=await api<Source>('/sources',jsonBody(sourceForm));notice='Источник «'+v.name+'» добавлен';sourceForm={name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:''};});}
 async function createJob(runNow:boolean){
  await runAction(async()=>{
   const days=scheduleForm.mode==='weekly'?weekdays.filter(x=>scheduleForm.weekdays.includes(x.id)).map(x=>x.id).join(','):'';
   const schedule={mode:scheduleForm.mode,interval_value:scheduleForm.mode==='interval'?scheduleForm.interval:0,
     interval_unit:scheduleForm.mode==='interval'?scheduleForm.unit:'',
     first_run:scheduleForm.startDate+'T'+scheduleForm.time,
     days_of_week:days,timezone:scheduleForm.timezone};
   const v=await api<Job>('/jobs',jsonBody({...jobForm,schedule,run_now:runNow}));
   notice=runNow?'Задание «'+v.name+'» сохранено и поставлено в очередь':'Задание «'+v.name+'» создано';
   jobForm={name:'',source_id:'',repository_id:''};
  });
 }
 async function jobAction(id:string,op:'run'|'pause'|'resume'){await runAction(async()=>{await api('/jobs/'+encodeURIComponent(id)+'/'+op,actionBody());notice=op==='run'?'Задание поставлено в очередь':op==='pause'?'Задание приостановлено':'Задание включено';});}
 async function openSnapshot(s:Snapshot){busy=true;error='';try{const v=await api<{files:File[]}>('/snapshots/'+encodeURIComponent(s.id)+'/files');files=v.files;activeSnapshot=s;selection=[];filter='';}
 catch(e){error=e instanceof Error?e.message:'Не удалось открыть снимок';}finally{busy=false;}}
 function toggleFile(path:string){selection=selection.includes(path)?selection.filter(x=>x!==path):[...selection,path];}
 function download(paths:string[],single=false){if(!activeSnapshot)return;const q=new URLSearchParams();for(const p of paths)q.append('path',p);if(single)q.set('mode','single');window.location.assign('/api/v1/backup/snapshots/'+encodeURIComponent(activeSnapshot.id)+'/download?'+q.toString());}
 async function restore(){if(!activeSnapshot||!window.confirm('Восстановить выбранное в новый каталог сервера? Существующие файлы не будут затронуты.'))return;
  await runAction(async()=>{const r=await api<{directory:string}>('/snapshots/'+encodeURIComponent(activeSnapshot!.id)+'/restore',jsonBody({paths:selection}));notice='Восстановлено в '+r.directory;});}
 function weekdayToggle(day:number,checked:boolean){scheduleForm.weekdays=checked?[...scheduleForm.weekdays,day]:scheduleForm.weekdays.filter(n=>n!==day);}
 onMount(()=>{
  scheduleForm.timezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC';
  const d=new Date();d.setDate(d.getDate()+1);
  scheduleForm.startDate=[d.getFullYear(),String(d.getMonth()+1).padStart(2,'0'),String(d.getDate()).padStart(2,'0')].join('-');
  void refresh();
 });
 $effect(()=>{if(section){editing=false;}});
</script>

<section class="work-area">
 <header class="workspace-header">
  <div><span class="eyebrow">{section==='overview'?'ПАНЕЛЬ УПРАВЛЕНИЯ':'BACKUPHUB / УПРАВЛЕНИЕ'}</span>
   <h1>{({overview:'Обзор',jobs:'Задания',sources:'Источники',storages:'Хранилища',history:'История копий',runs:'Журнал выполнения'} as Record<Section,string>)[section]}</h1>
   <p class="secondary">{({overview:'Состояние резервного копирования на одной странице.',jobs:'Что, куда и когда копировать.',sources:'Серверы, базы данных и каталоги для резервирования.',storages:'Места хранения резервных копий.',history:'Снимки, файлы и восстановление.',runs:'Все запуски и результаты резервного копирования.'} as Record<Section,string>)[section]}</p>
  </div>
  <div class="heading-actions"><button class="outline" onclick={()=>void refresh()} disabled={busy}>Обновить</button>
  {#if canConfigure&&['sources','jobs'].includes(section)}<button class="primary" onclick={()=>editing=!editing}>{editing?'Закрыть':'＋ '+(section==='sources'?'Источник':'Задание')}</button>{/if}</div>
 </header>
 {#if error}<div class="error" role="alert">{error}</div>{/if}
 {#if notice}<div class="notice" role="status">{notice}</div>{/if}
 {#if !loaded}
  <section class="detail-card">Загрузка данных…</section>
 {:else if section==='overview'}
  <div class="metrics">
   <div class="metric"><span>Активные задания</span><strong>{activeJobs.length}</strong><small>Всего заданий: {jobs.length}</small></div>
   <div class="metric"><span>Источники</span><strong>{sources.length}</strong><small>Зарегистрировано в системе</small></div>
   <div class="metric"><span>Снимки</span><strong>{snapshots.length}</strong><small>Доступны в истории</small></div>
   <div class="metric"><span>Ошибки в журнале</span><strong class:danger-text={failedRuns.length>0}>{failedRuns.length}</strong><small>Среди последних {runs.length} запусков</small></div>
  </div>
  <div class="two-column">
   <section class="detail-card">
    <div class="section-head"><h2>Задания</h2><button class="text-button" onclick={()=>onNavigate('jobs')}>Все задания →</button></div>
    {#if jobs.length===0}<div class="empty-state"><strong>Пока нет заданий</strong><p>Добавьте источник, затем создайте задание с хранилищем и периодичностью.</p><button class="outline" onclick={()=>onNavigate('jobs')}>Перейти к заданиям</button></div>
    {:else}<div class="compact-list">{#each jobs.slice(0,6) as j(j.id)}<div class="compact-row"><div><strong>{j.name}</strong><small>{j.source_name} · {storages.find(s=>s.id===j.repository_id)?.name??j.repository_id}</small></div><span class:muted={!j.enabled} class="status">{j.enabled?'Активно':'Пауза'}</span></div>{/each}</div>{/if}
   </section>
   <section class="detail-card">
    <div class="section-head"><h2>Последние запуски</h2><button class="text-button" onclick={()=>onNavigate('runs')}>Весь журнал →</button></div>
    {#if runs.length===0}<div class="empty-state"><strong>Запусков ещё не было</strong><p>Здесь появится история выполнения заданий.</p></div>
    {:else}<div class="compact-list">{#each runs.slice(0,6) as r(r.id)}<div class="compact-row"><div><strong>{jobs.find(j=>j.id===r.task_id)?.name??r.task_id.slice(0,8)}</strong><small>{fmt(r.started_at)}</small></div><span class:error-state={['failed','interrupted'].includes(r.state)} class:good-state={r.state==='completed'} class="status">{stateLabel(r.state)}</span></div>{/each}</div>{/if}
   </section>
  </div>
 {:else if section==='sources'}
  {#if editing&&canConfigure}
   <section class="detail-card editor-card"><div class="section-head"><h2>Новый источник</h2><span class="step-hint">1. Подключение</span></div>
    <form class="form-grid" onsubmit={(e)=>{e.preventDefault();void createSource();}}>
     <label>Название<input bind:value={sourceForm.name} maxlength="120" required placeholder="Файловый сервер бухгалтерии"/></label>
     <label>Тип источника<select bind:value={sourceForm.kind}><option value="smb">Сетевая папка SMB2/3</option><option value="sftp">Сервер Linux · SSH/SFTP</option><option value="local">Локальный каталог</option><option value="postgres">PostgreSQL</option></select></label>
     {#if sourceForm.kind!=='local'}<label>Адрес сервера<input bind:value={sourceForm.host} required placeholder={sourceForm.kind==='smb'?'fileserver.local':sourceForm.kind==='sftp'?'server.local:22':'db.local:5432'}/></label>
      <label>Пользователь<input bind:value={sourceForm.username} autocomplete="off" required/></label>
      <label>Пароль<input type="password" bind:value={sourceForm.password} autocomplete="new-password" required/></label>{/if}
     {#if sourceForm.kind==='smb'}<label>Общая папка<input bind:value={sourceForm.share} required placeholder="Shared"/></label><label>Подкаталог<input bind:value={sourceForm.path} placeholder="Accounting"/></label>
     {:else if sourceForm.kind==='postgres'}<label>База данных<input bind:value={sourceForm.database} required/></label>
     {:else}<label>Путь к каталогу<input bind:value={sourceForm.path} required placeholder="/srv/data"/></label>{/if}
     {#if sourceForm.kind==='sftp'}<p class="helper full">Для SSH необходим доверенный ключ хоста в /etc/backuphub/known_hosts.</p>{/if}
     <div class="form-actions"><button class="primary" type="submit" disabled={busy}>Сохранить источник</button><button class="outline" type="button" onclick={()=>editing=false}>Отмена</button></div>
    </form>
   </section>
  {/if}
  <section class="detail-card">
   <div class="section-head"><h2>Подключённые источники <span class="count">{sources.length}</span></h2></div>
   {#if sources.length===0}<div class="empty-state"><strong>Источников пока нет</strong><p>Добавьте каталог, файловый сервер или базу данных.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Название</th><th>Тип</th><th>Расположение</th><th>Заданий</th></tr></thead><tbody>{#each sources as s(s.id)}<tr><td><strong>{s.name}</strong></td><td><span class="type-label">{sourceType(s.kind)}</span></td><td class="wrap-cell">{sourcePath(s)}</td><td>{jobs.filter(j=>j.source_id===s.id).length}</td></tr>{/each}</tbody></table></div>{/if}
  </section>
 {:else if section==='jobs'}
  {#if editing&&canConfigure}
   <section class="detail-card editor-card">
    <div class="section-head"><h2>Новое задание</h2></div>
    {#if sources.length===0||availableStorages.length===0}
     <div class="empty-state"><strong>Нужны источник и доступное хранилище</strong>
      <p>Добавьте источник и укажите, куда сохранять резервные копии.</p>
      <div class="quick-links"><button class="outline" onclick={()=>onNavigate('sources')}>Источники</button><button class="outline" onclick={()=>onNavigate('storages')}>Хранилища</button></div></div>
    {:else}
     <form class="form-grid" onsubmit={(e)=>{e.preventDefault(); const submitter=(e as SubmitEvent).submitter as HTMLButtonElement|null;void createJob(submitter?.value==='run');}}>
      <label class="full">Название задания<input bind:value={jobForm.name} required maxlength="120" placeholder="Резервная копия бухгалтерии"/></label>
      <label>Источник<select bind:value={jobForm.source_id} required><option value="" disabled>Выберите источник</option>{#each sources as s(s.id)}<option value={s.id}>{s.name} · {sourceType(s.kind)}</option>{/each}</select></label>
      <label>Хранилище<select bind:value={jobForm.repository_id} required><option value="" disabled>Выберите хранилище</option>{#each availableStorages as s(s.id)}<option value={s.id}>{s.name}</option>{/each}</select></label>
      <div class="full schedule-heading"><h3>Периодичность</h3><p class="secondary">Настраивается здесь, отдельно создавать расписание не нужно.</p></div>
      <label class="full">Как часто выполнять
       <select bind:value={scheduleForm.mode}>
        <option value="daily">Каждый день в указанное время</option>
        <option value="weekly">В выбранные дни недели</option>
        <option value="interval">Каждые N минут, часов или дней</option>
       </select>
      </label>
      {#if scheduleForm.mode==='interval'}
       <label>Каждые<input type="number" min={scheduleForm.unit==='minutes'?5:1} max={scheduleForm.unit==='days'?365:scheduleForm.unit==='hours'?8760:525600} step="1" bind:value={scheduleForm.interval} required/></label>
       <label>Единицы<select bind:value={scheduleForm.unit}><option value="minutes">Минуты</option><option value="hours">Часы</option><option value="days">Дни</option></select></label>
      {:else if scheduleForm.mode==='weekly'}
       <div class="full"><span class="field-label">Дни недели</span><div class="weekday-picker">{#each weekdays as d(d.id)}<label><input type="checkbox" checked={scheduleForm.weekdays.includes(d.id)} onchange={(e)=>weekdayToggle(d.id,e.currentTarget.checked)}/>{d.label}</label>{/each}</div></div>
      {/if}
      <label>Дата начала<input type="date" bind:value={scheduleForm.startDate} required/></label>
      <label>Время первого запуска<input type="time" bind:value={scheduleForm.time} required/></label>
      <label class="full">Часовой пояс<input bind:value={scheduleForm.timezone} required placeholder="Europe/Moscow"/></label>
      <p class="helper full">{scheduleForm.mode==='interval'?'Первый плановый запуск — в указанную дату и время. Последующие запуски отсчитываются от неё, а не от завершения копирования. Для интервала в днях местное время сохраняется.':'Плановые запуски начнутся не раньше выбранной даты и времени.'} Ручной запуск доступен отдельно и не изменяет план.</p>
      <div class="form-actions">
       <button type="submit" value="save" class="outline" disabled={busy||(scheduleForm.mode==='weekly'&&scheduleForm.weekdays.length===0)}>Сохранить задание</button>
       <button type="submit" value="run" class="primary" disabled={busy||(scheduleForm.mode==='weekly'&&scheduleForm.weekdays.length===0)}>Сохранить и запустить</button>
       <button type="button" class="outline" onclick={()=>editing=false}>Отмена</button>
      </div>
     </form>
    {/if}
   </section>
  {/if}
  <section class="detail-card">
   <div class="section-head"><h2>Задания резервного копирования <span class="count">{jobs.length}</span></h2></div>
   {#if jobs.length===0}<div class="empty-state"><strong>Заданий пока нет</strong><p>Создайте задание: источник, хранилище и время запуска — всё в одной форме.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Задание</th><th>Хранилище</th><th>Периодичность</th><th>Следующий запуск</th><th>Состояние</th><th></th></tr></thead><tbody>
    {#each jobs as j(j.id)}<tr><td><strong>{j.name}</strong><small class="subline">{j.source_name}</small></td>
     <td>{storages.find(s=>s.id===j.repository_id)?.name??j.repository_id}</td>
     <td class="wrap-cell">{scheduleText(j.schedule)}</td>
     <td>{j.enabled?fmt(j.next_run):'—'}</td>
     <td><span class:muted={!j.enabled} class="status">{j.enabled?'Активно':'Пауза'}</span></td>
     <td class="action-cell">{#if canRun}<button class="text-button" onclick={()=>void jobAction(j.id,'run')} disabled={busy||!j.enabled}>Запустить сейчас</button>{/if}
      {#if canConfigure}<button class="text-button" onclick={()=>void jobAction(j.id,j.enabled?'pause':'resume')} disabled={busy}>{j.enabled?'Пауза':'Включить'}</button>{/if}</td>
    </tr>{/each}</tbody></table></div>{/if}
  </section>
 {:else if section==='storages'}
  {#if canConfigure}<StoragePane onCreated={refresh}/>{:else}
   <section class="detail-card"><h2>Хранилища</h2><div class="table-scroll"><table><thead><tr><th>Название</th><th>Путь</th><th>Место</th><th>Состояние</th></tr></thead><tbody>{#each storages as s(s.id)}<tr><td>{s.name}</td><td>{s.path}</td><td>{size(s.free_bytes)}</td><td>{s.status==='available'?'Доступно':'Недоступно'}</td></tr>{/each}</tbody></table></div></section>
  {/if}
 {:else if section==='runs'}
  <section class="detail-card"><div class="section-head"><h2>Последние запуски <span class="count">{runs.length}</span></h2></div>
   {#if runs.length===0}<div class="empty-state"><strong>Журнал пуст</strong><p>Результаты запусков появятся здесь.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Начало</th><th>Задание</th><th>Результат</th><th>Завершение</th><th>Код ошибки</th></tr></thead><tbody>{#each runs as r(r.id)}<tr><td>{fmt(r.started_at)}</td><td>{jobs.find(j=>j.id===r.task_id)?.name??r.task_id.slice(0,8)}</td><td><span class:error-state={['failed','interrupted'].includes(r.state)} class:good-state={r.state==='completed'} class="status">{stateLabel(r.state)}</span></td><td>{fmt(r.finished_at??'')}</td><td>{r.failure_code||'—'}</td></tr>{/each}</tbody></table></div>{/if}
  </section>
 {:else if section==='history'}
  <section class="detail-card"><div class="section-head"><h2>Снимки <span class="count">{snapshots.length}</span></h2></div>
   {#if snapshots.length===0}<div class="empty-state"><strong>Нет резервных копий</strong><p>После успешного выполнения задания здесь появится первый снимок.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Дата</th><th>Источник</th><th>Проверка</th><th></th></tr></thead><tbody>{#each snapshots as s(s.id)}<tr><td>{fmt(s.created_at)}</td><td><strong>{s.source_name}</strong></td><td><span class:good-state={s.verification==='data_sha256_verified'} class:error-state={s.verification!=='data_sha256_verified'} class="status">{s.verification==='data_sha256_verified'?'Проверено':'Требует проверки'}</span></td><td><button class="outline" disabled={busy} onclick={()=>void openSnapshot(s)}>Открыть снимок</button></td></tr>{/each}</tbody></table></div>{/if}
  </section>
  {#if activeSnapshot}<section class="detail-card">
   <div class="section-head"><div><h2>Файлы — {activeSnapshot.source_name}</h2><p class="secondary">Снимок от {fmt(activeSnapshot.created_at)}</p></div><button class="text-button" onclick={()=>{activeSnapshot=null;files=[];selection=[];}}>Закрыть</button></div>
   <label class="search-field">Поиск по пути<input bind:value={filter} placeholder="Название файла или папки"/></label>
   {#if canExport}<div class="form-actions"><button class="primary" onclick={()=>download(selection)} disabled={busy||selection.length===0}>Скачать выбранное ({selection.length})</button><button class="outline" onclick={()=>download([])}>Скачать весь снимок</button><button class="outline" onclick={()=>void restore()} disabled={busy||selection.length===0}>Восстановить выбранное</button></div>{/if}
   <div class="table-scroll file-scroll"><table><thead><tr><th></th><th>Путь</th><th>Тип</th><th>Размер</th><th>Изменён</th><th></th></tr></thead><tbody>{#each visibleFiles.slice(0,2000) as f(f.path)}<tr><td><input type="checkbox" checked={selection.includes(f.path)} onchange={()=>toggleFile(f.path)} disabled={!canExport||f.type==='other'}/></td><td class="wrap-cell">{f.path}</td><td>{f.type==='dir'?'Папка':f.type==='file'?'Файл':'Другое'}</td><td>{f.type==='file'?new Intl.NumberFormat('ru-RU').format(f.size):'—'}</td><td>{fmt(f.modified)}</td><td>{#if canExport&&f.type==='file'}<button class="text-button" onclick={()=>download([f.path],true)}>Скачать</button>{/if}</td></tr>{/each}</tbody></table></div>
   {#if visibleFiles.length>2000}<p class="secondary">Показаны первые 2000 записей. Уточните поиск.</p>{/if}
  </section>{/if}
 {/if}
</section>

<style>
 .work-area{display:grid;gap:22px}
 .workspace-header{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;margin-bottom:3px}
 .workspace-header h1{margin:7px 0 6px}
 .workspace-header p{margin:0}
 .heading-actions,.form-actions,.quick-links{display:flex;flex-wrap:wrap;gap:9px;align-items:center}
 .metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:15px}
 .metric{background:#fff;border:1px solid var(--line);border-radius:15px;padding:22px;display:flex;flex-direction:column;gap:11px;box-shadow:var(--shadow)}
 .metric span{font-size:13px;color:var(--muted);font-weight:600}
 .metric strong{font-size:31px;line-height:1.2;letter-spacing:-.04em}
 .metric small{font-size:12px;color:var(--muted)}
 .two-column{display:grid;grid-template-columns:1fr 1fr;gap:18px;align-items:start}
 .section-head{display:flex;align-items:center;justify-content:space-between;gap:18px;flex-wrap:wrap;margin-bottom:17px}
 .section-head h2{margin:0}
 .section-head .secondary{margin:5px 0 0}
 .count{font-size:12px;font-weight:700;color:var(--muted);border:1px solid var(--line);padding:3px 8px;border-radius:20px;vertical-align:middle;margin-left:5px}
 .step-hint{color:var(--muted);font-size:12px}
 .form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}
 .form-grid label{display:flex;flex-direction:column;gap:8px;font-weight:600;font-size:13px;color:var(--text)}
 .form-grid .full,.form-actions,.full{grid-column:1/-1}
 .form-actions{margin-top:7px}
 .field-label{display:block;font-weight:600;font-size:13px;margin-bottom:10px}
 .schedule-heading{padding-top:7px;border-top:1px solid var(--line)}
 .schedule-heading h3{margin:0 0 5px;font-size:16px}
 .schedule-heading p{margin:0}
 .weekday-picker{display:flex;flex-wrap:wrap;gap:8px}
 .weekday-picker label{display:flex;flex-direction:row;align-items:center;gap:7px;border:1px solid var(--line);padding:10px;border-radius:9px;cursor:pointer}
 .weekday-picker input{width:auto}
 .empty-state{padding:34px 18px;display:flex;flex-direction:column;align-items:center;gap:10px;text-align:center;color:var(--text)}
 .empty-state strong{font-size:16px}
 .empty-state p{color:var(--muted);margin:0 0 8px}
 .compact-list{display:grid}
 .compact-row{padding:14px 0;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;gap:14px;align-items:center}
 .compact-row:last-child{border-bottom:0}
 .compact-row div{min-width:0;display:grid;gap:6px}
 .compact-row strong{font-size:13px;overflow-wrap:anywhere}
 .compact-row small,.subline{font-size:12px;color:var(--muted);display:block;margin-top:5px}
 .type-label{font-weight:650;color:#3c5f89}
 .wrap-cell{max-width:360px;min-width:120px;white-space:normal;overflow-wrap:anywhere;font-size:12px;color:var(--muted)}
 .action-cell{white-space:nowrap;text-align:right}
 .action-cell button+button{margin-left:10px}
 .search-field{display:grid;gap:7px;max-width:420px;margin-bottom:14px}
 .file-scroll{max-height:550px;overflow:auto}
 .helper{font-size:12px;color:var(--muted)}
 .status{display:inline-flex;align-items:center;white-space:nowrap;border-radius:7px;padding:5px 10px;color:#256348;background:#eaf7f0;font-size:11px;font-weight:700}
 .status.muted{color:#788394;background:#f1f3f7}
 .status.error-state{color:#a84242;background:#fcecec}
 .status.good-state{color:#256348;background:#eaf7f0}
 .danger-text{color:#b44343}
 @media(max-width:1100px){.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.two-column{grid-template-columns:1fr}}
 @media(max-width:700px){.workspace-header{align-items:flex-start;flex-direction:column}.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.form-grid{grid-template-columns:1fr}.metric{padding:15px}}
</style>
