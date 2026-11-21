<script lang="ts">
 import {onMount} from 'svelte';
 import NavIcon from './NavIcon.svelte';
 type Folder={path:string;name:string;type:string};
 type Page={files:Folder[];has_more:boolean};
 type Cache={items:Folder[];more:boolean;offset:number;loading:boolean};
 type Node={key:string;path:string;name:string;depth:number;more:boolean};
 let {snapshotId,activePath,onNavigate}:{snapshotId:string;activePath:string;onNavigate:(path:string)=>void}=$props();
 let expanded=$state<string[]>(['.']);
 let cache=$state<Record<string,Cache>>({});
 let error=$state('');
 const limit=100;

 function flatten():Node[]{
  const out:Node[]=[];
  const descend=(path:string,depth:number)=>{
   out.push({key:path,path,name:path==='.'?'Корень снимка':path.split('/').at(-1)??path,depth,more:false});
   if(!expanded.includes(path))return;
   const state=cache[path];
   if(state){
    for(const child of state.items)descend(child.path,depth+1);
    if(state.more)out.push({key:path+'~more',path,name:'Показать ещё папки…',depth:depth+1,more:true});
   }
  };
  descend('.',0);
  return out;
 }
 const nodes=$derived(flatten());
 async function loadNext(path:string){
  const current=cache[path];
  if(current?.loading||current&&!current.more)return;
  const offset=current?.offset??0;
  cache={...cache,[path]:{items:current?.items??[],more:false,offset,loading:true}};
  const qs=new URLSearchParams({dir:path,folders:'1',limit:String(limit),offset:String(offset)});
  try{
   const response=await fetch('/api/v1/backup/snapshots/'+encodeURIComponent(snapshotId)+'/files?'+qs.toString(),{credentials:'same-origin'});
   const body=await response.json().catch(()=>null);
   if(!response.ok)throw new Error(body?.error??'Не удалось загрузить папки');
   const page=body as Page;
   const previous=current?.items??[];
   const seen=new Set(previous.map(x=>x.path));
   const items=[...previous,...page.files.filter(x=>x.type==='dir'&&!seen.has(x.path))];
   cache={...cache,[path]:{items,more:page.has_more,offset:offset+page.files.length,loading:false}};
   error='';
  }catch(e){
   cache={...cache,[path]:{items:current?.items??[],more:true,offset,loading:false}};
   error=e instanceof Error?e.message:'Не удалось открыть дерево каталогов';
  }
 }
 function toggle(path:string){
  if(expanded.includes(path)){
   expanded=expanded.filter(x=>x!==path);
  }else{
   expanded=[...expanded,path];
   if(!cache[path])void loadNext(path);
  }
 }
 function open(path:string){
  onNavigate(path);
  if(!expanded.includes(path))expanded=[...expanded,path];
  if(!cache[path])void loadNext(path);
 }
 $effect(()=>{
  const target=activePath;
  const parts=target==='.'?[]:target.split('/');
  const ancestors=['.'];
  for(let i=0;i<parts.length;i++)ancestors.push(parts.slice(0,i+1).join('/'));
  let changed=false;
  for(const path of ancestors){
   if(!expanded.includes(path)){expanded=[...expanded,path];changed=true;}
   if(!cache[path])void loadNext(path);
  }
  void changed;
 });
 onMount(()=>{if(!cache['.'])void loadNext('.');});
</script>

<aside class="folder-tree" aria-label="Дерево каталогов">
 <div class="tree-heading">ПАПКИ</div>
 <div class="tree-list">
  {#each nodes as node(node.key)}
   {#if node.more}
    <button class="more-folders" type="button" style:padding-left={(node.depth*14+8)+'px'} onclick={()=>void loadNext(node.path)} disabled={cache[node.path]?.loading}>
     {cache[node.path]?.loading?'Загрузка…':'Показать ещё папки…'}
    </button>
   {:else}
    <div class:active={node.path===activePath} class="tree-item" style:padding-left={(node.depth*14+5)+'px'}>
     <button class="expand" type="button" aria-label={expanded.includes(node.path)?'Свернуть папку':'Раскрыть папку'} onclick={()=>toggle(node.path)}>{expanded.includes(node.path)?'⌄':'›'}</button>
     <button class="tree-name" type="button" title={node.path} onclick={()=>open(node.path)}><span><NavIcon name="folder" size={16}/></span> {node.name}</button>
     {#if cache[node.path]?.loading}<span class="loading-indicator" aria-label="Загрузка">·</span>{/if}
    </div>
   {/if}
  {/each}
 </div>
 {#if error}<p class="tree-error" role="alert">{error}</p>{/if}
</aside>
<style>
 .folder-tree{border:1px solid var(--line,#e1e7ef);border-radius:10px;background:#fafbfd;display:flex;flex-direction:column;min-width:0;max-height:65vh;overflow:hidden}
 .tree-heading{font-size:10px;letter-spacing:.1em;font-weight:800;color:#8191a6;padding:15px 12px 12px;border-bottom:1px solid var(--line,#e1e7ef)}
 .tree-list{padding:7px 4px;overflow:auto;min-height:100px;flex:1}
 .tree-item{display:flex;align-items:center;gap:2px;min-height:31px;padding-top:2px;padding-bottom:2px;border-radius:6px;white-space:nowrap;min-width:0}
 .tree-item:hover{background:#edf3fb}
 .tree-item.active{background:#e7f0ff;color:#245fae}
 .expand{border:0;background:transparent;width:19px;flex-shrink:0;text-align:center;color:#70839e;cursor:pointer;padding:3px;font-size:16px}
 .tree-name{border:0;background:transparent;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;color:inherit;text-align:left;cursor:pointer;padding:4px 0;flex:1}
 .tree-name span{color:#5281bc;margin-right:3px}
 .loading-indicator{font-weight:800;color:#6d91bf}
 .more-folders{border:0;background:transparent;color:#426fad;font-size:12px;cursor:pointer;text-align:left;padding-top:7px;padding-bottom:7px}
 .tree-error{padding:6px 11px;font-size:11px;color:#a33c3c;overflow-wrap:anywhere}
</style>
