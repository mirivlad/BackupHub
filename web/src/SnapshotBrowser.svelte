<script lang="ts">
 import {onMount} from 'svelte';
 import SnapshotFolderTree from './SnapshotFolderTree.svelte';
 import NavIcon from './NavIcon.svelte';

 type Snapshot={id:string;source_name:string;created_at:string};
 type FileEntry={path:string;name:string;type:'file'|'dir'|'other';size:number;modified:string};
 type BrowsePage={files:FileEntry[];directory:string;search?:string;offset:number;limit:number;has_more:boolean};
 let {snapshot,canExport,onClose}:{snapshot:Snapshot;canExport:boolean;onClose:()=>void}=$props();

 const pageSize=120;
 const maxSelection=64;
 let directory=$state('.');
 let searchDraft=$state('');
 let searchTerm=$state('');
 let pageIndex=$state(0);
 let entries=$state<FileEntry[]>([]);
 let hasMore=$state(false);
 let selected=$state<string[]>([]);
 let loading=$state(true);
 let error=$state('');
 let notice=$state('');
 let restoreBusy=$state(false);
 let requestId=0;
 let controller:AbortController|null=null;
 let listElement:HTMLDivElement|undefined;

 const breadcrumbs=$derived((()=>{
  const crumbs=[{name:'Корень снимка',path:'.'}];
  if(directory==='.')return crumbs;
  const fragments=directory.split('/');
  let path='';
  for(const fragment of fragments){
   path=path?path+'/'+fragment:fragment;
   crumbs.push({name:fragment,path});
  }
  return crumbs;
 })());
 const available=$derived(entries.filter(e=>e.type==='file'||e.type==='dir'));
 const wholePageSelected=$derived(available.length>0&&available.every(e=>selected.includes(e.path)));
 const offset=$derived(pageIndex*pageSize);
 const displayedRange=$derived(entries.length===0?'Нет элементов':String(offset+1)+'–'+String(offset+entries.length));

 const formatDate=(s:string)=>{const date=new Date(s);return Number.isNaN(date.getTime())?'—':date.toLocaleString('ru-RU',{dateStyle:'short',timeStyle:'short'});};
 const formatBytes=(bytes:number)=>{if(!Number.isFinite(bytes))return '—';if(bytes<1024)return new Intl.NumberFormat('ru-RU').format(bytes)+' Б';const units=['КиБ','МиБ','ГиБ','ТиБ'];let count=bytes;let idx=-1;do{count/=1024;idx++;}while(count>=1024&&idx<units.length-1);return new Intl.NumberFormat('ru-RU',{maximumFractionDigits:1}).format(count)+' '+units[idx];};
 const getFolder=(path:string)=>{const i=path.lastIndexOf('/');return i<0?'.':path.slice(0,i);};
 function download(paths:string[],single=false){
  const url=new URL('/api/v1/backup/snapshots/'+encodeURIComponent(snapshot.id)+'/download',window.location.origin);
  for(const item of paths)url.searchParams.append('path',item);
  if(single)url.searchParams.set('mode','single');
  const link=document.createElement('a');link.href=url.pathname+url.search;link.style.display='none';
  document.body.appendChild(link);link.click();link.remove();
 }
 async function load(){
  controller?.abort();
  controller=new AbortController();
  const thisRequest=++requestId;
  loading=true;error='';
  const params=new URLSearchParams({dir:directory,offset:String(pageIndex*pageSize),limit:String(pageSize)});
  if(searchTerm)params.set('q',searchTerm);
  try{
   const response=await fetch('/api/v1/backup/snapshots/'+encodeURIComponent(snapshot.id)+'/files?'+params.toString(),{
    credentials:'same-origin',signal:controller.signal
   });
   const body=await response.json().catch(()=>null);
   if(!response.ok)throw new Error(body?.error||'Не удалось загрузить содержимое снимка');
   if(thisRequest!==requestId)return;
   const listing=body as BrowsePage;
   entries=listing.files;
   hasMore=listing.has_more;
   listElement?.scrollTo({top:0});
  }catch(err){
   if(thisRequest!==requestId)return;
   if(err instanceof Error&&err.name==='AbortError')return;
   error=err instanceof Error?err.message:'Ошибка загрузки';
   entries=[];hasMore=false;
  }finally{if(thisRequest===requestId)loading=false;}
 }
 function navigate(path:string){
  directory=path;
  searchTerm='';searchDraft='';pageIndex=0;
  void load();
 }
 function parentFolder(){if(directory!=='.')navigate(getFolder(directory));}
 function setPage(next:number){if(next<0)return;pageIndex=next;void load();}
 function runSearch(){
  const text=searchDraft.trim();
  if(text.length===1){error='Для поиска введите хотя бы два символа';return;}
  if(text===searchTerm&&pageIndex===0)return;
  searchTerm=text;pageIndex=0;void load();
 }
 function clearSearch(){searchTerm='';searchDraft='';pageIndex=0;void load();}
 function toggle(path:string){
  if(selected.includes(path)){selected=selected.filter(x=>x!==path);return;}
  if(selected.length>=maxSelection){error='Можно выбрать до 64 позиций. Для большого набора скачайте целую папку или весь снимок.';return;}
  selected=[...selected,path];error='';
 }
 function togglePage(){
  if(wholePageSelected){
   const visible=new Set(available.map(e=>e.path));
   selected=selected.filter(p=>!visible.has(p));
   return;
  }
  const next=[...selected];
  for(const item of available){
   if(next.includes(item.path))continue;
   if(next.length===maxSelection)break;
   next.push(item.path);
  }
  selected=next;
  if(available.length>maxSelection)notice='Выбраны первые 64 позиции. Для всего каталога проще скачать папку целиком.';
 }
 function downloadCurrentFolder(){
  download(directory==='.'?[]:[directory]);
 }
 function csrf(){
  return decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');
 }
 async function restoreSelected(){
  if(!selected.length||!canExport)return;
  if(!window.confirm('Восстановить '+selected.length+' выбранных позиций в новый каталог сервера? Исходные файлы не будут перезаписаны.'))return;
  restoreBusy=true;error='';notice='';
  try{
   const response=await fetch('/api/v1/backup/snapshots/'+encodeURIComponent(snapshot.id)+'/restore',{
    method:'POST',credentials:'same-origin',
    headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},
    body:JSON.stringify({paths:selected})
   });
   const body=await response.json().catch(()=>null);
   if(!response.ok)throw new Error(body?.error||'Восстановление не выполнено');
   notice='Восстановлено на сервере: '+body.directory;
  }catch(err){error=err instanceof Error?err.message:'Ошибка восстановления';}
  finally{restoreBusy=false;}
 }
 onMount(()=>{
  void load();
  return ()=>{requestId++;controller?.abort();};
 });
</script>

<section class="snapshot-browser" aria-label="Просмотр содержимого резервной копии">
 <div class="browser-top">
  <div>
   <div class="browser-kicker">СОДЕРЖИМОЕ СНИМКА</div>
   <h2>{snapshot.source_name}</h2>
   <p class="browser-subtitle">{formatDate(snapshot.created_at)} · Можно открывать папки и скачивать файлы без восстановления всего снимка</p>
  </div>
  <button class="close-browser" type="button" onclick={onClose} title="Закрыть просмотр">Закрыть <span aria-hidden="true">×</span></button>
 </div>
 <div class="browser-toolbar">
  <nav class="breadcrumbs" aria-label="Путь к папке">
   {#each breadcrumbs as crumb,i(crumb.path)}
    {#if i>0}<span class="crumb-divider" aria-hidden="true">›</span>{/if}
    {#if i===breadcrumbs.length-1}<span class="crumb-current" title={crumb.name}>{crumb.name}</span>
    {:else}<button type="button" class="crumb-link" onclick={()=>navigate(crumb.path)} title={crumb.name}>{crumb.name}</button>{/if}
   {/each}
  </nav>
  <form class="browser-search" onsubmit={(e)=>{e.preventDefault();runSearch();}}>
   <input aria-label="Поиск по всему снимку" bind:value={searchDraft} placeholder="Найти файл или папку во всём снимке…" />
   <button type="submit" class="button-blue">Найти</button>
   {#if searchTerm}<button type="button" class="button-neutral" onclick={clearSearch}>Сбросить</button>{/if}
  </form>
 </div>
 <div class="browser-actions">
  {#if !searchTerm}<button class="button-neutral" type="button" onclick={parentFolder} disabled={directory==='.'||loading}>↑ На уровень выше</button>
  {:else}<span class="search-caption">Результаты поиска: «{searchTerm}» по всему снимку</span>{/if}
  {#if canExport}
   <button type="button" class="button-primary" onclick={()=>download(selected)} disabled={selected.length===0||loading}>Скачать выбранное {selected.length>0?'('+selected.length+')':''} ZIP</button>
   <button type="button" class="button-neutral" onclick={downloadCurrentFolder} disabled={loading}>{directory==='.'?'Скачать весь снимок ZIP':'Скачать текущую папку ZIP'}</button>
   <button type="button" class="button-neutral" onclick={()=>void restoreSelected()} disabled={selected.length===0||restoreBusy||loading}>{restoreBusy?'Восстановление…':'Восстановить выбранное'}</button>
   {#if selected.length>0}<button type="button" class="clear-selection" onclick={()=>selected=[]}>Снять выделение</button>{/if}
  {/if}
 </div>
 {#if selected.length>0}
  <div class="selection-tip" role="status">Выбрано {selected.length} из {maxSelection} позиций. Выбор сохраняется при переходе между папками.</div>
 {/if}
 {#if error}<div class="browser-error" role="alert">{error}</div>{/if}
 {#if notice}<div class="browser-notice" role="status">{notice}</div>{/if}
 <div class="browser-content">
  <SnapshotFolderTree snapshotId={snapshot.id} activePath={directory} onNavigate={navigate}/>
  <div class="browser-files">
  <div class="browser-list" bind:this={listElement} aria-busy={loading}>
  <div class="file-columns">
   <div>{#if canExport}<input aria-label="Выделить позиции на странице" type="checkbox" checked={wholePageSelected} disabled={loading||available.length===0} onchange={togglePage}/>{/if}</div>
   <div>Имя</div><div>Размер</div><div>Изменён</div><div></div>
  </div>
  {#if loading}
   <div class="browser-empty">Загружается содержимое папки…</div>
  {:else if entries.length===0}
   <div class="browser-empty">{searchTerm?'Совпадений не найдено':'В этой папке нет файлов'}</div>
  {:else}
   {#each entries as entry(entry.path)}
    <div class:selected-row={selected.includes(entry.path)} class="file-item">
     <div class="item-check">{#if canExport}<input type="checkbox" aria-label={'Выбрать '+entry.name} disabled={entry.type==='other'} checked={selected.includes(entry.path)} onchange={()=>toggle(entry.path)}/>{/if}</div>
     <div class="file-identity">
      <span class="file-icon"><NavIcon name={entry.type==='dir'?'folder':entry.type==='file'?'file':'file-other'} size={18}/></span>
      {#if entry.type==='dir'}
       <button type="button" class="entry-name folder-name" onclick={()=>navigate(entry.path)} title={entry.path}>{entry.name}</button>
      {:else}
       <span class="entry-name" title={entry.path}>{entry.name}</span>
      {/if}
      {#if searchTerm}<small class="entry-path" title={entry.path}>{entry.path}</small>{/if}
     </div>
     <div class="file-size">{entry.type==='file'?formatBytes(entry.size):'—'}</div>
     <div class="file-time">{formatDate(entry.modified)}</div>
     <div class="file-buttons">
      {#if entry.type==='dir'}
       <button type="button" class="item-link" onclick={()=>navigate(entry.path)}>Открыть</button>
       {#if canExport}<button type="button" class="item-link secondary" onclick={()=>download([entry.path])}>ZIP</button>{/if}
      {:else if entry.type==='file'&&canExport}
       <button type="button" class="item-link" onclick={()=>download([entry.path],true)}>Скачать</button>
      {/if}
     </div>
    </div>
   {/each}
  {/if}
 </div>
 <footer class="browser-footer">
  <span>{loading?'Загрузка…':displayedRange+' на странице'}{searchTerm?' · поиск по всему снимку':''}</span>
  <div class="page-actions">
   <button type="button" class="button-neutral" disabled={loading||pageIndex===0} onclick={()=>setPage(pageIndex-1)}>← Назад</button>
   <span>Страница {pageIndex+1}</span>
   <button type="button" class="button-neutral" disabled={loading||!hasMore} onclick={()=>setPage(pageIndex+1)}>Вперёд →</button>
  </div>
 </footer>
  </div>
 </div>
</section>

<style>
 .snapshot-browser{display:grid;gap:14px;color:var(--text,#15223a)}
 .browser-content{display:grid;grid-template-columns:minmax(210px,240px) minmax(0,1fr);gap:14px;align-items:start}
 .browser-files{display:grid;gap:12px;min-width:0}

 .browser-top{display:flex;justify-content:space-between;align-items:flex-start;gap:15px}
 .browser-top h2{font-size:20px;margin:3px 0 3px;font-weight:700;line-height:1.3}
 .browser-kicker{color:#6283b6;font-size:10px;font-weight:800;letter-spacing:.1em}
 .browser-subtitle{font-size:12px;color:var(--muted,#64748b);margin:5px 0 0}
 .close-browser{flex-shrink:0;border:0;background:transparent;font-weight:650;color:#416ab1;cursor:pointer;font-size:13px;padding:6px}
 .close-browser span{font-size:19px;vertical-align:middle;margin-left:4px}
 .browser-toolbar{display:grid;gap:12px;background:#f7f9fd;border:1px solid var(--line,#dfe5ed);border-radius:10px;padding:13px}
 .breadcrumbs{display:flex;flex-wrap:wrap;gap:7px;align-items:center;min-width:0}
 .crumb-divider{color:#93a2b9;font-size:19px}
 .crumb-current{font-weight:720;color:#203452;overflow-wrap:anywhere}
 .crumb-link{background:none;border:0;cursor:pointer;padding:3px 0;color:#346ac0;font-size:13px}
 .browser-search{display:flex;gap:8px;max-width:720px}
 .browser-search input{flex:1;min-width:0;background:#fff;border:1px solid var(--line,#dfe5ed);border-radius:8px;padding:10px 13px;font-size:13px;color:var(--text,#14233e)}
 .browser-actions{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
 button.button-primary,button.button-blue{border:1px solid #2c64bd;background:#326bcc;color:#fff;border-radius:8px;padding:9px 13px;font-weight:650;cursor:pointer;font-size:12px}
 button.button-neutral{border:1px solid var(--line,#dce3ec);background:#fff;color:#345178;border-radius:8px;padding:9px 12px;cursor:pointer;font-size:12px;font-weight:600}
 button:disabled{opacity:.45;cursor:not-allowed}
 button.clear-selection{border:0;background:none;color:#57759e;font-size:12px;cursor:pointer}
 .search-caption{font-size:12px;color:#597396}
 .selection-tip{background:#f0f6ff;padding:10px 12px;border-radius:8px;color:#365d90;font-size:12px}
 .browser-error{border:1px solid #f3b5b5;border-radius:8px;color:#a03939;background:#fff2f2;padding:11px;font-size:12px}
 .browser-notice{border:1px solid #cbe9d5;border-radius:8px;background:#effaf3;color:#226d46;padding:11px;font-size:12px;overflow-wrap:anywhere}
 .browser-list{border:1px solid var(--line,#e2e8f0);border-radius:10px;max-height:65vh;overflow:auto}
 .file-columns,.file-item{display:grid;grid-template-columns:38px minmax(0,1fr) 110px 145px 125px;align-items:center;gap:10px;padding:10px 12px}
 .file-columns{font-size:11px;color:#8794a8;background:#f6f8fb;font-weight:700;position:sticky;top:0;z-index:1}
 .file-item{min-height:48px;border-top:1px solid #eef1f5;font-size:13px}
 .file-item:hover{background:#f7faff}
 .file-item.selected-row{background:#f0f6ff}
 .item-check input,.file-columns input{width:16px;height:16px;accent-color:#356fc9;vertical-align:middle}
 .file-identity{display:flex;align-items:center;gap:10px;min-width:0;flex-wrap:wrap}
 .file-icon{font-size:17px;width:20px;text-align:center;color:#3e79cc}
 .entry-name{font-weight:550;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:100%}
 button.entry-name{padding:3px 0;border:0;background:none;text-align:left;cursor:pointer;color:#255caa;font-size:13px;font-weight:700}
 button.entry-name:hover{text-decoration:underline}
 .entry-path{flex-basis:100%;padding-left:30px;font-size:11px;color:#7d8999;overflow-wrap:anywhere}
 .file-size,.file-time{color:#63748b;font-size:12px;white-space:nowrap}
 .file-buttons{display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end}
 .item-link{padding:4px 0;border:0;background:none;color:#306abd;font-size:12px;cursor:pointer}
 .item-link.secondary{color:#6b7e9d}
 .item-link:hover{text-decoration:underline}
 .browser-empty{padding:44px 15px;text-align:center;color:#64748b;font-size:13px}
 .browser-footer{display:flex;gap:12px;justify-content:space-between;align-items:center;flex-wrap:wrap;color:#8290a5;font-size:12px}
 .page-actions{display:flex;align-items:center;gap:12px}
 @media(max-width:850px){.browser-content{grid-template-columns:1fr}.browser-content :global(.folder-tree){max-height:230px}}
 @media(max-width:900px){.file-columns,.file-item{grid-template-columns:30px minmax(0,1fr) 86px 105px}.file-columns>div:nth-child(4),.file-item>.file-time{display:none}.file-buttons{min-width:0}}
 @media(max-width:590px){.file-columns,.file-item{grid-template-columns:28px minmax(0,1fr) 78px}.file-columns>div:nth-child(3),.file-item>.file-size{display:none}.browser-search{flex-wrap:wrap}.browser-search input{flex-basis:100%}.file-buttons{gap:5px}}
</style>
