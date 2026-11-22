<script lang="ts">
 import {onMount,tick} from 'svelte';
 import StoragePane from './StoragePane.svelte';
 import CronRuleEditor from './CronRuleEditor.svelte';
 import SnapshotBrowser from './SnapshotBrowser.svelte';
 import {formatDuration} from './duration';
 import {checkDate,restoreCheckLabel,checkTone,type CheckStatus} from './verification';
 type Section='overview'|'jobs'|'sources'|'storages'|'history'|'runs';
 type Source={id:string;name:string;kind:string;host:string;share:string;path:string;username:string;database:string;access_status?:string;access_hint?:string;auth_method?:string;host_key_fingerprint?:string;};
 type Storage={id:string;name:string;path:string;free_bytes:number;status:string;password_mode:string};
 type Profile={mode:'interval'|'daily'|'weekly'|'cron';interval_value:number;interval_unit:string;days_of_week:string;hour:number;minute:number;timezone:string;first_run:string;cron_expression?:string};
 type Job={id:string;name:string;source_id:string;source_name:string;repository_id:string;schedule:Profile;enabled:boolean;next_run:string;running:boolean;manual_queued:boolean;current_state?:string; retry_count:number; retry_interval_minutes:number; operation_id?:string; attempt?:number; next_retry?:string; wait_reason?:string; reconciling?:boolean};
 type Snapshot={id:string;source_id:string;source_name:string;kind:string;created_at:string;verification:string;fidelity:string;consistency:string;duration_ms?:number;restore_check:CheckStatus;can_test_restore:boolean};
 type Run={id:string;task_id:string;state:string;started_at:string;finished_at?:string;failure_code?:string;snapshot_id?:string; operation_id?:string; attempt:number; failure_stage?:string; failure_detail?:string; operation_state?:string; operation_failure?:string; next_retry?:string};
 let {section,role,jobToEdit='',onNavigate}:{section:Section;role:string;jobToEdit?:string;onNavigate:(section:Section)=>void}=$props();
 let sources=$state<Source[]>([]);
 let storages=$state<Storage[]>([]);
 let jobs=$state<Job[]>([]);
 let snapshots=$state<Snapshot[]>([]);
 let runs=$state<Run[]>([]);
 let activeSnapshot=$state<Snapshot|null>(null);
 let snapshotPanel=$state<HTMLElement>();
 let busy=$state(false);
 let loaded=$state(false);
 let error=$state('');
 let notice=$state('');
 let editing=$state(false);
 let editingSourceID=$state<string|null>(null);
 let editingJobID=$state<string|null>(null);
 let jobEditor=$state<HTMLElement>();
 let cronExpr=$state('0 2 * * *');
 let previewTimes=$state<string[]>([]);
 let previewError=$state('');

 let sourceForm=$state({name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:'',auth_method:'password',private_key:'',key_passphrase:''});
 let jobForm=$state({name:'',source_id:'',repository_id:'',retry_count:3,retry_interval_minutes:30});
 let scheduleForm=$state({mode:'daily' as 'daily'|'weekly'|'interval'|'cron',time:'02:00',startDate:'',timezone:'UTC',interval:7,unit:'days',weekdays:[1,2,3,4,5] as number[]});
 const weekdays=[{id:1,label:'Пн'},{id:2,label:'Вт'},{id:3,label:'Ср'},{id:4,label:'Чт'},{id:5,label:'Пт'},{id:6,label:'Сб'},{id:0,label:'Вс'}];
 const canConfigure=$derived(role==='administrator');
 const canRun=$derived(role==='administrator'||role==='backup_operator');
 const canExport=$derived(role==='administrator'||role==='restore_operator');
 const availableStorages=$derived(storages.filter(s=>s.status==='available'));
 const activeJobs=$derived(jobs.filter(j=>j.enabled));
 const failedRuns=$derived(runs.filter(r=>r.state==='failed'||r.state==='interrupted'));
 function elapsed(v:string){
  const age=Math.max(0,Math.floor((Date.now()-new Date(v).getTime())/1000));
  if(!Number.isFinite(age))return '';
  return age>=3600?Math.floor(age/3600)+' ч '+Math.floor((age%3600)/60)+' мин':Math.floor(age/60)+' мин '+age%60+' с';
 }
 function activeRun(job:Job){
  return runs.find(r=>r.task_id===job.id&&!r.finished_at);
 }
 const fmt=(v:string)=>{if(!v)return '—';const d=new Date(v);return Number.isNaN(d.getTime())?'—':d.toLocaleString('ru-RU',{dateStyle:'short',timeStyle:'short'});};
 const size=(bytes:number)=>new Intl.NumberFormat('ru-RU',{maximumFractionDigits:1}).format(bytes/1073741824)+' ГиБ';
 function sourceType(kind:string){return ({smb:'SMB',smb1:'SMB',sftp:'SFTP',postgres:'PostgreSQL',postgres_1c:'Postgres Pro для 1С',local:'Локальный каталог'} as Record<string,string>)[kind]??kind;}
 function sourcePath(s:Source){return ['smb','smb1'].includes(s.kind)?'//'+s.host+'/'+s.share+(s.path?'/'+s.path:''):['postgres','postgres_1c'].includes(s.kind)?s.host+'/'+s.database:s.kind==='sftp'?s.host+':'+s.path:s.path;}
 function stateLabel(state:string){return ({completed:'Завершено',completed_with_warnings:'С предупреждениями',failed:'Ошибка',queued:'В очереди',preflight:'Проверка',preparing:'Подготовка',capturing:'Копирование',committing:'Сохранение',verifying:'Проверка данных',publishing:'Публикация манифеста',finalizing:'Запись в каталог',reconciling:'Проверка после сбоя',waiting_for_source:'Ожидание источника',interrupted:'Прервано',cancelled:'Отменено'} as Record<string,string>)[state]??state;}
 function runTone(state:string){
  if(state==='completed')return 'good-state';
  if(['failed','interrupted'].includes(state))return 'error-state';
  if(['queued','waiting_for_source','completed_with_warnings'].includes(state))return 'warn-state';
  if(state==='cancelled')return 'muted';
  return 'active-state';
 }
 function runDuration(run:Run){
  return run.finished_at
   ?formatDuration(Date.parse(run.finished_at)-Date.parse(run.started_at))
   :elapsed(run.started_at)||'—';
 }
 function scheduleText(p:Profile){
  if(!p)return '—';
  const at=String(p.hour).padStart(2,'0')+':'+String(p.minute).padStart(2,'0');
  if(p.mode==='cron')return 'Гибкое расписание · '+p.timezone;
  if(p.mode==='daily')return 'Каждый день в '+at+' · '+p.timezone;
  if(p.mode==='weekly')return p.days_of_week.split(',').map(n=>weekdays.find(x=>x.id===Number(n))?.label??n).join(', ')+' в '+at+' · '+p.timezone;
  const units:Record<string,string>={minutes:'мин.',hours:'ч.',days:'дн.'};
  return 'Каждые '+p.interval_value+' '+(units[p.interval_unit]??p.interval_unit)+' с '+p.first_run.replace('T',' ')+' · '+p.timezone;
 }
 function failureText(code:string){
  const descriptions:Record<string,string>={
   source_permission_denied:'Нет прав на чтение источника (ACL / ограничения systemd)',
   ssh_host_key_changed:'Ключ SSH-сервера изменился: соединение заблокировано. Сверьте отпечаток сервера.',
   source_unavailable:'Источник отсутствует или скрыт ограничениями службы',
   storage_unavailable:'Хранилище недоступно. Проверьте состояние тома и репозитория',
   executor_error:'Ошибка во время копирования (подробности в журнале службы)',
   capture_failed:'Не удалось прочитать или подготовить данные источника',
   restic_interrupted:'Процесс Restic завершился во время попытки',
   restic_backup_failed:'Restic не смог записать резервную копию в хранилище',
   restic_partial_backup:'Restic прочитал не все данные. Снимок не добавлен в историю',
   restic_verification_failed:'Не удалось проверить целостность данных в хранилище',
   repository_busy:'Хранилище занято другой операцией. Задание будет повторено.',
   restic_repository_locked:'Хранилище занято другой операцией Restic или блокировка недоступна для снятия',
   restic_manifest_failed:'Не удалось сохранить или проверить метаданные снимка',
   snapshot_catalog_failed:'Снимок записан, но его не удалось добавить в каталог',
   process_restart:'Копирование прервано остановкой службы',
   retry_exhausted:'Лимит повторов исчерпан',
   retry_cancelled:'Ожидающий повтор отменён',
   network_unavailable:'Соединение с источником прервано',
   operation_timeout:'Истекло время ожидания подключения или передачи данных',
   storage_full:'Недостаточно места на целевом диске',
   repository_integrity:'Нарушена целостность репозитория',
   source_authentication_failed:'Источник отклонил учётные данные',
   storage_authentication_failed:'Не удалось открыть репозиторий с сохранёнными ключами',
   source_configuration:'Проверьте настройки источника и наличие программ для копирования',
   storage_configuration:'Проверьте настройки хранилища и наличие Restic'
  };
  return descriptions[code]??(code||'—');
 }
 function jobState(j:Job){
  if(j.running||j.reconciling)return stateLabel(j.current_state||'queued');
  if(!j.enabled)return 'Пауза';
  if(j.next_retry){
   if(j.wait_reason==='source_unavailable'||j.wait_reason==='network_unavailable')return 'Ожидание источника';
   if(j.wait_reason==='storage_unavailable'||j.wait_reason==='storage_full')return 'Ожидание хранилища';
   return 'Ожидание повтора';
  }
  return j.manual_queued?'Запуск в очереди':'По расписанию';
 }
 function schedulePayload():Profile{
  const days=scheduleForm.mode==='weekly'?weekdays.filter(x=>scheduleForm.weekdays.includes(x.id)).map(x=>x.id).join(','):'';
  return {mode:scheduleForm.mode,interval_value:scheduleForm.mode==='interval'?scheduleForm.interval:0,
   interval_unit:scheduleForm.mode==='interval'?scheduleForm.unit:'',
   first_run:scheduleForm.startDate+'T'+(scheduleForm.mode==='cron'?'00:00':scheduleForm.time),
   days_of_week:days,timezone:scheduleForm.timezone,cron_expression:scheduleForm.mode==='cron'?cronExpr:'' ,hour:0,minute:0};
 }
 function startNew(kind:'sources'|'jobs'){
  editing=true;previewTimes=[];previewError='';
  if(kind==='sources'){
   editingSourceID=null;
   sourceForm={name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:'',auth_method:'password',private_key:'',key_passphrase:''};
  }else{
   editingJobID=null;jobForm={name:'',source_id:'',repository_id:'',retry_count:3,retry_interval_minutes:30};
   scheduleForm.mode='daily';scheduleForm.time='02:00';scheduleForm.interval=7;scheduleForm.unit='days';
   cronExpr='0 2 * * *';
  }
 }
 function beginEditSource(s:Source){
  editing=true;editingSourceID=s.id;
  sourceForm={name:s.name,kind:s.kind==='smb1'?'smb':s.kind,host:s.host,share:s.share,path:s.path,username:s.username,password:'',database:s.database,auth_method:s.auth_method||'password',private_key:'',key_passphrase:''};
 }
 function beginEditJob(j:Job){
  editing=true;editingJobID=j.id;previewTimes=[];previewError='';
  jobForm={name:j.name,source_id:j.source_id,repository_id:j.repository_id,retry_count:j.retry_count,retry_interval_minutes:j.retry_interval_minutes};
  scheduleForm.mode=j.schedule.mode;
  scheduleForm.interval=j.schedule.interval_value||7;
  scheduleForm.unit=j.schedule.interval_unit||'days';
  scheduleForm.time=j.schedule.first_run?.slice(11,16)||String(j.schedule.hour).padStart(2,'0')+':'+String(j.schedule.minute).padStart(2,'0');
  scheduleForm.startDate=j.schedule.first_run?.slice(0,10)||scheduleForm.startDate;
  scheduleForm.timezone=j.schedule.timezone||'UTC';
  scheduleForm.weekdays=(j.schedule.days_of_week||'').split(',').filter(x=>x!=='').map(Number);
  cronExpr=j.schedule.cron_expression||'0 2 * * *';
 }
 async function previewSchedule(){
  previewError='';previewTimes=[];
  try{
   const result=await api<{next_runs:string[]}>('/jobs/preview',jsonBody(schedulePayload()));
   previewTimes=result.next_runs;
  }catch(e){previewError=e instanceof Error?e.message:'Проверьте расписание';}
 }
 function csrf(){return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
 async function api<T>(path:string,opts:RequestInit={}):Promise<T>{const r=await fetch('/api/v1/backup'+path,{credentials:'same-origin',...opts});const body=await r.json().catch(()=>null);
  if(!r.ok)throw new Error(body?.error??'Ошибка запроса: HTTP '+r.status);return body as T;}
 function jsonBody(value:unknown):RequestInit{return {method:'POST',headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},body:JSON.stringify(value)};}
 function actionBody():RequestInit{return {method:'POST',headers:{'X-BackupHub-CSRF':csrf()}};}
 async function refresh(){
  busy=true;error='';
  try{
   const responses=await Promise.allSettled([
    api<{sources:Source[]}>('/sources'),
    api<{storages:Storage[]}>('/storages'),
    api<{jobs:Job[]}>('/jobs'),
    api<{snapshots:Snapshot[]}>('/snapshots'),
    api<{runs:Run[]}>('/runs')
   ]);
   if(responses[0].status==='fulfilled')sources=responses[0].value.sources;
   if(responses[1].status==='fulfilled')storages=responses[1].value.storages;
   if(responses[2].status==='fulfilled')jobs=responses[2].value.jobs;
   if(responses[3].status==='fulfilled')snapshots=responses[3].value.snapshots;
   if(responses[4].status==='fulfilled')runs=responses[4].value.runs;
   const names=['Источники','Хранилища','Задания','История копий','Журнал выполнения'];
   const failures=responses.flatMap((r,i)=>r.status==='rejected'?[names[i]+': '+(r.reason instanceof Error?r.reason.message:'ошибка запроса')]:[]);
   if(failures.length>0)error=failures.join('; ');
   loaded=true;
  }catch(e){error=e instanceof Error?e.message:'Данные недоступны';}
  finally{busy=false;}
 }
 async function refreshRuntime(){
  // Poll only the lightweight SQLite endpoints; never force restic list/
  // storage discovery while a backup is running.
  try{
   const [j,r]=await Promise.all([
    api<{jobs:Job[]}>('/jobs'),
    api<{runs:Run[]}>('/runs')
   ]);
   const hasNewSnapshot=r.runs.some(next=>next.snapshot_id&&!runs.some(previous=>previous.id===next.id&&previous.snapshot_id===next.snapshot_id));
   jobs=j.jobs;runs=r.runs;
   if(section==='history'||hasNewSnapshot)await refreshSnapshots();
  }catch{ /* Keep the last known state; manual Refresh gives diagnostics. */ }
 }
 async function refreshSnapshots(){
  snapshots=(await api<{snapshots:Snapshot[]}>('/snapshots')).snapshots;
  if(activeSnapshot)activeSnapshot=snapshots.find(snapshot=>snapshot.id===activeSnapshot?.id)??activeSnapshot;
 }
 async function showSnapshot(snapshot:Snapshot){
  activeSnapshot=snapshot;
  await tick();
  snapshotPanel?.scrollIntoView({block:'start'});
 }
 async function openRunSnapshot(id:string){
  error='';
  try{
   await showSnapshot(await api<Snapshot>('/snapshots/'+encodeURIComponent(id)));
   onNavigate('history');
  }catch(e){error=e instanceof Error?e.message:'Снимок недоступен';}
 }
 async function runAction(fn:()=>Promise<void>){busy=true;error='';notice='';
  try{await fn();editing=false;await refresh();}catch(e){error=e instanceof Error?e.message:'Операция не выполнена';}
  finally{busy=false;}
 }
 async function saveSource(){
  await runAction(async()=>{
   const opts=jsonBody(sourceForm);
   const result=await api<Source>(editingSourceID?'/sources/'+encodeURIComponent(editingSourceID):'/sources',
    editingSourceID?{...opts,method:'PUT'}:opts);
   notice='Источник «'+result.name+'» сохранён';
   editingSourceID=null;sourceForm={name:'',kind:'smb',host:'',share:'',path:'',username:'',password:'',database:'',auth_method:'password',private_key:'',key_passphrase:''};
  });
 }
 async function saveJob(){
  await runAction(async()=>{
   const opts=jsonBody({...jobForm,schedule:schedulePayload()});
   const result=await api<Job>(editingJobID?'/jobs/'+encodeURIComponent(editingJobID):'/jobs',
    editingJobID?{...opts,method:'PUT'}:opts);
   notice='Задание «'+result.name+'» сохранено';
   editingJobID=null;jobForm={name:'',source_id:'',repository_id:'',retry_count:3,retry_interval_minutes:30};previewTimes=[];
  });
 }
 async function changeRetry(job:Job,cancel:boolean){
  await runAction(async()=>{
   await api('/jobs/'+encodeURIComponent(job.id)+(cancel?'/cancel-retry':'/retry'),jsonBody({operation_id:job.operation_id}));
   notice=cancel?'Ожидающий повтор отменён':'Повтор готов к запуску';
  });
 }
 async function jobAction(id:string,op:'run'|'pause'|'resume'){await runAction(async()=>{await api('/jobs/'+encodeURIComponent(id)+'/'+op,actionBody());notice=op==='run'?'Запуск в очереди. Результат появится в журнале выполнения.':op==='pause'?'Задание приостановлено':'Задание включено';});}
 function weekdayToggle(day:number,checked:boolean){scheduleForm.weekdays=checked?[...scheduleForm.weekdays,day]:scheduleForm.weekdays.filter(n=>n!==day);}
 onMount(()=>{
  scheduleForm.timezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC';
  const d=new Date();d.setDate(d.getDate()+1);
  scheduleForm.startDate=[d.getFullYear(),String(d.getMonth()+1).padStart(2,'0'),String(d.getDate()).padStart(2,'0')].join('-');
  const initialJobID=jobToEdit;
  void refresh().then(async()=>{
   if(!initialJobID||section!=='jobs'||!canConfigure)return;
   const job=jobs.find(item=>item.id===initialJobID);
   if(!job){
    if(!error)error='Задание больше недоступно';
    return;
   }
   beginEditJob(job);
   await tick();
   jobEditor?.scrollIntoView({block:'start'});
   jobEditor?.querySelector('input')?.focus({preventScroll:true});
  });
  const poll=setInterval(()=>{if(!busy)void refreshRuntime();},5000);
  return ()=>clearInterval(poll);
 });
 $effect(()=>{
  const scheduleKey=[scheduleForm.mode,scheduleForm.time,scheduleForm.startDate,scheduleForm.timezone,scheduleForm.interval,scheduleForm.unit,scheduleForm.weekdays.join(','),cronExpr].join('|');
  void scheduleKey;
  previewTimes=[];previewError='';
 });
 $effect(()=>{
  editing=false;
  notice='';
  if(section==='history')void refreshSnapshots().catch(e=>{error=e instanceof Error?e.message:'История недоступна';});
 });
</script>

<section class="work-area">
 <header class="workspace-header">
  <div><span class="eyebrow">{section==='overview'?'ПАНЕЛЬ УПРАВЛЕНИЯ':'BACKUPHUB / УПРАВЛЕНИЕ'}</span>
   <h1>{({overview:'Обзор',jobs:'Задания',sources:'Источники',storages:'Хранилища',history:'История копий',runs:'Журнал выполнения'} as Record<Section,string>)[section]}</h1>
   <p class="secondary">{({overview:'Состояние резервного копирования на одной странице.',jobs:'Что, куда и когда копировать.',sources:'Серверы, базы данных и каталоги для резервирования.',storages:'Места хранения резервных копий.',history:'Снимки, файлы и восстановление.',runs:'Все запуски и результаты резервного копирования.'} as Record<Section,string>)[section]}</p>
  </div>
  <div class="heading-actions"><button class="outline" onclick={()=>void refresh()} disabled={busy}>Обновить</button>
  {#if canConfigure&&['sources','jobs'].includes(section)}<button class="primary" onclick={()=>editing?editing=false:startNew(section as 'sources'|'jobs')}>{editing?'Закрыть':'＋ '+(section==='sources'?'Источник':'Задание')}</button>{/if}</div>
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
    {:else}<div class="compact-list">{#each jobs.slice(0,6) as j(j.id)}<div class="compact-row"><div><strong>{j.name}</strong><small>{j.source_name} · {storages.find(s=>s.id===j.repository_id)?.name??j.repository_id}</small></div><span class:muted={!j.enabled} class:good-state={j.enabled} class="status">{j.enabled?'Активно':'Пауза'}</span></div>{/each}</div>{/if}
   </section>
   <section class="detail-card">
    <div class="section-head"><h2>Последние запуски</h2><button class="text-button" onclick={()=>onNavigate('runs')}>Весь журнал →</button></div>
    {#if runs.length===0}<div class="empty-state"><strong>Запусков ещё не было</strong><p>Здесь появится история выполнения заданий.</p></div>
    {:else}<div class="compact-list">{#each runs.slice(0,6) as r(r.id)}<div class="compact-row"><div><strong>{jobs.find(j=>j.id===r.task_id)?.name??r.task_id.slice(0,8)}</strong><small>{fmt(r.started_at)}</small></div><span class={'status '+runTone(r.state)}>{stateLabel(r.state)}</span></div>{/each}</div>{/if}
   </section>
  </div>
 {:else if section==='sources'}
  {#if editing&&canConfigure}
   <section class="detail-card editor-card">
    <div class="section-head"><h2>{editingSourceID?'Редактирование источника':'Новый источник'}</h2></div>
    <form class="form-grid" onsubmit={(e)=>{e.preventDefault();void saveSource();}}>
     <label>Название<input bind:value={sourceForm.name} maxlength="120" required placeholder="Документы"/></label>
     <label>Тип источника<select bind:value={sourceForm.kind}><option value="smb">Сетевая папка SMB</option><option value="sftp">Сервер Linux · SSH/SFTP</option><option value="local">Локальный каталог</option><option value="postgres">PostgreSQL</option><option value="postgres_1c">Postgres Pro для 1С</option></select></label>
     {#if sourceForm.kind!=='local'}
      <label>Адрес сервера<input bind:value={sourceForm.host} required placeholder="server.example.com"/></label>
      <label>Пользователь<input bind:value={sourceForm.username} autocomplete="off" required/></label>
      {#if sourceForm.kind==='sftp'}
       <label class="full">Способ входа
        <select bind:value={sourceForm.auth_method}>
         <option value="password">Логин и пароль</option>
         <option value="key">Логин и приватный SSH-ключ</option>
        </select>
       </label>
       {#if sourceForm.auth_method==='key'}
        <label class="full">Приватный ключ
         <textarea rows="7" bind:value={sourceForm.private_key} required={!editingSourceID} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea>
         <small class="helper">Поддерживаются OpenSSH и PuTTY PPK (версии 2 и 3). Ключ хранится в зашифрованном хранилище BackupHub.</small>
        </label>
        <label class="full">Загрузить файл приватного ключа
         <input type="file" accept=".key,.pem,.ppk,.txt" onchange={async(e)=>{const file=e.currentTarget.files?.[0];if(file){if(file.size>65536){error='Файл ключа превышает 64 КиБ';return;}sourceForm.private_key=await file.text();}}}/>
        </label>
        <label class="full">Парольная фраза к приватному ключу
         <input type="password" bind:value={sourceForm.key_passphrase} autocomplete="new-password" placeholder="Не требуется для незашифрованного ключа"/>
         {#if editingSourceID}<small class="helper">Для замены парольной фразы загрузите ключ повторно.</small>{/if}
        </label>
       {:else}
        <label class="full">Пароль SSH
         <input type="password" bind:value={sourceForm.password} autocomplete="new-password" required={!editingSourceID}/>
         {#if editingSourceID}<small class="helper">Пустое значение сохраняет прежний пароль.</small>{/if}
        </label>
       {/if}
       <p class="helper full">При первом подключении BackupHub автоматически запомнит публичный ключ сервера (TOFU). При изменении ключа соединение будет заблокировано. Сохранённый отпечаток SHA-256 отображается в списке источников.</p>
      {:else}
       <label>Пароль<input type="password" bind:value={sourceForm.password} autocomplete="new-password" required={!editingSourceID}/>
        {#if editingSourceID}<small class="helper">Пустое значение сохраняет прежний пароль.</small>{/if}
       </label>
      {/if}
     {/if}
     {#if sourceForm.kind==='smb'}
      <label>Общая папка<input bind:value={sourceForm.share} required placeholder="Shared"/></label>
      <label>Подкаталог<input bind:value={sourceForm.path} placeholder="Documents"/></label>
      <p class="helper full">Версия определяется автоматически. Если сервер поддерживает SMB2 или SMB3, BackupHub использует современный протокол. Для сервера только с SMB1 нужны NTLMv2 и подписывание.</p>
     {:else if ['postgres','postgres_1c'].includes(sourceForm.kind)}
      <label>База данных<input bind:value={sourceForm.database} required/></label>
      {#if sourceForm.kind==='postgres_1c'}<p class="helper full">Данные public.config сохраняются отдельным двоичным потоком. Восстановление выполняется в совместимую отдельную базу.</p>{/if}
     {:else}
      <label class="full">Путь к каталогу<input bind:value={sourceForm.path} required placeholder="/home/user/.config"/></label>
     {/if}
     {#if sourceForm.kind==='local'}
      <p class="helper full">BackupHub работает от пользователя <strong>backuphub</strong> и должен иметь права на чтение источника. Для домашних каталогов может потребоваться настройка ACL.</p>
     {/if}

     <div class="form-actions"><button class="primary" type="submit" disabled={busy}>{editingSourceID?'Сохранить изменения':'Добавить источник'}</button><button class="outline" type="button" onclick={()=>editing=false}>Отмена</button></div>
    </form>
   </section>
  {/if}
  <section class="detail-card">
   <div class="section-head"><h2>Источники <span class="count">{sources.length}</span></h2></div>
   {#if sources.length===0}<div class="empty-state"><strong>Источников пока нет</strong><p>Добавьте каталог, файловый сервер или базу данных.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Название</th><th>Тип</th><th>Расположение</th><th>Доступ</th><th>Заданий</th><th></th></tr></thead><tbody>
    {#each sources as s(s.id)}<tr><td><strong>{s.name}</strong></td><td><span class="type-label">{sourceType(s.kind)}</span></td>
     <td class="wrap-cell">{sourcePath(s)}
      {#if s.kind==='sftp'}<small class="subline">{s.host_key_fingerprint?'Ключ сервера: '+s.host_key_fingerprint:'Ключ сервера запомнится при первом подключении'}</small>{/if}
     </td>
     <td>{#if s.kind==='local'}
        {#if s.access_status==='ok'}<span class="status good-state">Доступен</span>
        {:else}<span class="status error-state" title={s.access_hint||'Проверьте доступ к источнику'}>{s.access_status==='denied'?'Нет прав':s.access_status==='missing'?'Не найден':'Ошибка доступа'}</span><small class="subline">{s.access_hint}</small>{/if}
      {:else}<span class="secondary">Не проверялся</span>{/if}</td>
     <td>{jobs.filter(j=>j.source_id===s.id).length}</td>
     <td class="action-cell">{#if canConfigure}<button class="text-button" onclick={()=>beginEditSource(s)}>Изменить</button>{/if}</td>
    </tr>{/each}
   </tbody></table></div>{/if}
  </section>
 {:else if section==='jobs'}
  {#if editing&&canConfigure}
   <section class="detail-card editor-card" bind:this={jobEditor}>
    <div class="section-head"><h2>{editingJobID?'Редактирование задания':'Новое задание'}</h2></div>
    {#if sources.length===0||availableStorages.length===0}
     <div class="empty-state"><strong>Нужны источник и доступное хранилище</strong>
      <p>Добавьте источник и укажите, куда сохранять резервные копии.</p>
      <div class="quick-links"><button class="outline" onclick={()=>onNavigate('sources')}>Источники</button><button class="outline" onclick={()=>onNavigate('storages')}>Хранилища</button></div></div>
    {:else}
     <form class="form-grid" onsubmit={(e)=>{e.preventDefault();void saveJob();}}>
      <label class="full">Название задания<input bind:value={jobForm.name} required maxlength="120" placeholder="Бэкап рабочих файлов"/></label>
      <label>Источник<select bind:value={jobForm.source_id} required><option value="" disabled>Выберите источник</option>{#each sources as s(s.id)}<option value={s.id}>{s.name} · {sourceType(s.kind)}</option>{/each}</select></label>
      <label>Хранилище<select bind:value={jobForm.repository_id} required><option value="" disabled>Выберите хранилище</option>{#each availableStorages as s(s.id)}<option value={s.id}>{s.name}</option>{/each}</select></label>
      <label>Количество повторных попыток<input type="number" min="0" max="10" step="1" required bind:value={jobForm.retry_count}/></label>
      <label>Шаг увеличения интервала, мин<input type="number" min="1" max="525600" step="1" required bind:value={jobForm.retry_interval_minutes}/></label>
      <div class="full schedule-heading"><h3>Расписание</h3><p class="secondary">Все настройки выполняются в этом задании.</p></div>
      <label class="full">Режим планирования
       <select bind:value={scheduleForm.mode} onchange={()=>{previewTimes=[];previewError='';}}>
        <option value="daily">Каждый день</option>
        <option value="weekly">По дням недели</option>
        <option value="interval">Через заданный интервал</option>
        <option value="cron">Гибкий конструктор (как cron)</option>
       </select>
      </label>
      {#if scheduleForm.mode==='interval'}
       <label>Период<input type="number" min={scheduleForm.unit==='minutes'?5:1} max={scheduleForm.unit==='days'?365:scheduleForm.unit==='hours'?8760:525600} step="1" bind:value={scheduleForm.interval} required/></label>
       <label>Единицы<select bind:value={scheduleForm.unit}><option value="minutes">Минуты</option><option value="hours">Часы</option><option value="days">Дни</option></select></label>
      {:else if scheduleForm.mode==='weekly'}
       <div class="full"><span class="field-label">Дни недели</span><div class="weekday-picker">{#each weekdays as d(d.id)}<label><input type="checkbox" checked={scheduleForm.weekdays.includes(d.id)} onchange={(e)=>weekdayToggle(d.id,e.currentTarget.checked)}/>{d.label}</label>{/each}</div></div>
      {:else if scheduleForm.mode==='cron'}
       <CronRuleEditor value={cronExpr} onChange={(v)=>{cronExpr=v;previewTimes=[];previewError='';}}/>
      {/if}
      <label>{scheduleForm.mode==='cron'?'Начать использовать правило с':'Дата первого запуска'}<input type="date" bind:value={scheduleForm.startDate} required/></label>
      {#if scheduleForm.mode!=='cron'}
       <label>Время запуска<input type="time" bind:value={scheduleForm.time} required/></label>
      {/if}
      <label class="full">Часовой пояс<input bind:value={scheduleForm.timezone} required placeholder="Europe/Moscow"/></label>
      <p class="helper full">В режиме интервала отсчёт идёт от выбранной даты. Ручной запуск из списка заданий не изменяет график. В гибком режиме работают правила минут, часов, дней, месяцев и дней недели.</p>
      <div class="preview-block full">
       <button type="button" class="outline" onclick={()=>void previewSchedule()} disabled={busy||(scheduleForm.mode==='weekly'&&scheduleForm.weekdays.length===0)}>Показать ближайшие 5 запусков</button>
       {#if previewError}<p class="error">{previewError}</p>{/if}
       {#if previewTimes.length>0}<div class="preview-list">{#each previewTimes as next}<span>{fmt(next)}</span>{/each}</div>{/if}
      </div>
      <div class="form-actions">
       <button type="submit" class="primary" disabled={busy||(scheduleForm.mode==='weekly'&&scheduleForm.weekdays.length===0)}>{editingJobID?'Сохранить изменения':'Создать задание'}</button>
       <button type="button" class="outline" onclick={()=>editing=false}>Отмена</button>
      </div>
     </form>
    {/if}
   </section>
  {/if}
  <section class="detail-card">
   <div class="section-head"><h2>Задания резервного копирования <span class="count">{jobs.length}</span></h2></div>
   {#if jobs.length===0}<div class="empty-state"><strong>Заданий пока нет</strong><p>Создайте задание, выберите источник, хранилище и расписание.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Задание</th><th>Хранилище</th><th>Периодичность</th><th>Следующий запуск</th><th>Состояние</th><th></th></tr></thead><tbody>
    {#each jobs as j(j.id)}<tr><td><strong>{j.name}</strong><small class="subline">{j.source_name}</small></td>
     <td>{storages.find(s=>s.id===j.repository_id)?.name??j.repository_id}</td>
     <td class="wrap-cell">{scheduleText(j.schedule)}</td>
     <td>{j.enabled?fmt(j.next_run):'—'}</td>
     <td>
      <span class:muted={!j.enabled} class:active-state={j.running} class:warn-state={j.enabled&&!j.running&&(!!j.next_retry||j.manual_queued||j.reconciling)} class:good-state={j.enabled&&!j.running&&!j.operation_id&&!j.manual_queued} class="status">{jobState(j)}</span>
      {#if j.attempt}<small class="subline">Попытка {j.attempt} из {j.retry_count+1}</small>{/if}
      {#if j.next_retry}
       <small class="subline">{failureText(j.wait_reason||'')}</small>
       <small class="subline">Повтор {fmt(j.next_retry)}</small>
      {/if}
      {#if j.running&&activeRun(j)}
       <small class="subline">Выполняется: {elapsed(activeRun(j)!.started_at)}</small>
      {/if}
     </td>
     <td class="action-cell">
      {#if canRun}
       {#if j.next_retry}
        <button class="text-button" onclick={()=>void changeRetry(j,false)} disabled={busy||!j.enabled}>Повторить сейчас</button>
        <button class="text-button" onclick={()=>void changeRetry(j,true)} disabled={busy}>Отменить ожидающий повтор</button>
       {:else}
        <button class="text-button" onclick={()=>void jobAction(j.id,'run')} disabled={busy||!j.enabled||j.running||j.manual_queued||!!j.operation_id}>Запустить сейчас</button>
       {/if}
      {/if}
      {#if canConfigure}<button class="text-button" onclick={()=>beginEditJob(j)} disabled={busy||j.running||!!j.operation_id}>Изменить</button>
      <button class="text-button" onclick={()=>void jobAction(j.id,j.enabled?'pause':'resume')} disabled={busy}>{j.enabled?'Пауза':'Включить'}</button>{/if}</td>
    </tr>{/each}</tbody></table></div>{/if}
  </section>
 {:else if section==='storages'}
  <StoragePane onCreated={refresh} {canConfigure} {canRun}/>
 {:else if section==='runs'}
  <section class="detail-card"><div class="section-head"><h2>Последние запуски <span class="count">{runs.length}</span></h2></div>
   {#if runs.length===0}<div class="empty-state"><strong>Журнал пуст</strong><p>Результаты запусков появятся здесь.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Начало</th><th>Задание</th><th>Результат</th><th>Завершение</th><th>Длительность</th><th>Подробности</th></tr></thead><tbody>
    {#each runs as r(r.id)}
     <tr>
      <td>{fmt(r.started_at)}</td>
      <td>{jobs.find(j=>j.id===r.task_id)?.name??r.task_id.slice(0,8)}
       {#if r.operation_id}<small class="subline" title={r.operation_id}>Операция {r.operation_id.slice(0,8)} · попытка {r.attempt}</small>{/if}
      </td>
      <td><span class={'status '+runTone(r.state)}>{stateLabel(r.state)}</span></td>
      <td>{fmt(r.finished_at??'')}</td>
      <td>{runDuration(r)}</td>
      <td class="run-detail">
       {#if r.failure_code}
        {failureText(r.failure_code)}
        {#if r.failure_stage}<small class="subline">{stateLabel(r.failure_stage)}</small>{/if}
       {/if}
       {#if r.operation_failure==='retry_exhausted'||r.operation_failure==='retry_cancelled'}
        <small class="subline">{failureText(r.operation_failure)}</small>
       {/if}
       {#if r.snapshot_id&&(r.state==='completed'||r.operation_state==='completed')}
        {#if r.attempt>1||r.state!=='completed'}<small class="subline">Копирование завершено после сбоя</small>{/if}
        <button class="text-button" onclick={()=>void openRunSnapshot(r.snapshot_id!)}>Открыть снимок</button>
       {/if}
       {#if r.next_retry&&!r.snapshot_id&&r.operation_state==='waiting'}
        <small class="subline">Следующий повтор {fmt(r.next_retry)}</small>
       {/if}
      </td>
     </tr>
    {/each}
   </tbody></table></div>{/if}
  </section>
 {:else if section==='history'}
  <section class="detail-card"><div class="section-head"><h2>Снимки <span class="count">{snapshots.length}</span></h2></div>
   {#if snapshots.length===0}<div class="empty-state"><strong>Нет резервных копий</strong><p>После успешного выполнения задания здесь появится первый снимок.</p></div>
   {:else}<div class="table-scroll"><table><thead><tr><th>Дата</th><th>Источник</th><th>Длительность</th><th>Целостность при записи</th><th>Пробное восстановление</th><th></th></tr></thead><tbody>{#each snapshots as s(s.id)}<tr><td>{fmt(s.created_at)}</td><td><strong>{s.source_name}</strong></td><td>{formatDuration(s.duration_ms)}</td><td><span class:good-state={s.verification==='data_sha256_verified'} class:warn-state={s.verification!=='data_sha256_verified'} class="status">{s.verification==='data_sha256_verified'?'Проверено':'Требует проверки'}</span></td><td>{#if s.can_test_restore}
      <span class={'check-badge '+checkTone(s.restore_check.state)} title={s.restore_check.finished_at ? checkDate(s.restore_check.finished_at) : ''}>{restoreCheckLabel(s.restore_check.state)}</span>
     {:else}<span class="secondary">Для файловых источников</span>{/if}</td><td><button class="outline" disabled={busy} onclick={()=>void showSnapshot(s)}>Открыть снимок</button></td></tr>{/each}</tbody></table></div>{/if}
  </section>
  {#if activeSnapshot}
   <section class="detail-card" bind:this={snapshotPanel}>
    {#key activeSnapshot.id}
     <SnapshotBrowser snapshot={activeSnapshot} {canExport} onClose={()=>activeSnapshot=null}/>
    {/key}
   </section>
  {/if}
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
 .count{font-size:12px;font-weight:700;color:var(--muted);border:1px solid var(--line);padding:3px 8px;border-radius:20px;vertical-align:middle;margin-left:5px}
 .form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}
 .form-grid label{display:flex;flex-direction:column;gap:8px;font-weight:600;font-size:13px;color:var(--text)}
 .form-grid .full,.form-actions,.full{grid-column:1/-1}
 .form-actions{margin-top:7px}
 .field-label{display:block;font-weight:600;font-size:13px;margin-bottom:10px}
 .schedule-heading{padding-top:7px;border-top:1px solid var(--line)}
 .schedule-heading h3{margin:0 0 5px;font-size:16px}
 .schedule-heading p{margin:0}
 .preview-block{display:grid;gap:10px;justify-items:start}
 .preview-list{display:flex;gap:8px;flex-wrap:wrap}
 .preview-list span{border:1px solid #d5e1f5;border-radius:7px;background:#f5f9ff;padding:7px 11px;font-size:12px;color:#305c98}
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
 .helper{font-size:12px;color:var(--muted)}
 .run-detail{min-width:145px;white-space:normal}
 .status{display:inline-flex;align-items:center;white-space:nowrap;border-radius:7px;padding:5px 10px;color:#fff;background:#48576e;font-size:11px;font-weight:750}
 .status.muted{background:#48576e}
 .status.error-state{background:#a22230}
 .status.good-state{background:#146541}
 .status.warn-state{background:#865010}
 .status.active-state{background:#2252a0}
 .danger-text{color:#a22230}
 @media(max-width:1100px){.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.two-column{grid-template-columns:1fr}}
 @media(max-width:700px){.workspace-header{align-items:flex-start;flex-direction:column}.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.form-grid{grid-template-columns:1fr}.metric{padding:15px}}
</style>
