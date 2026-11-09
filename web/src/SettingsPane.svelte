<script lang="ts">
 import {onMount} from 'svelte';
 type Mode='password'|'no_password';
 let saved=$state<Mode>('password');
 let selected=$state<Mode>('password');
 let ready=$state(false);
 let busy=$state(false);
 let error=$state('');
 let notice=$state('');
 function csrf():string{return decodeURIComponent(document.cookie.split('; ').find(x=>x.startsWith('backuphub_csrf='))?.slice('backuphub_csrf='.length)??'');}
 async function load(){
  busy=true;error='';
  try{
   const response=await fetch('/api/v1/system/settings',{credentials:'same-origin'});
   const data=await response.json();
   if(!response.ok)throw new Error(data.error??'Ошибка загрузки настроек');
   saved=data.default_repository_password_mode as Mode;
   selected=saved;ready=true;
  }catch(e){error=e instanceof Error?e.message:'Настройки недоступны';}
  finally{busy=false;}
 }
 async function save(){
  busy=true;error='';notice='';
  try{
   const response=await fetch('/api/v1/system/settings',{
    method:'POST',credentials:'same-origin',
    headers:{'Content-Type':'application/json','X-BackupHub-CSRF':csrf()},
    body:JSON.stringify({default_repository_password_mode:selected})
   });
   const data=await response.json();
   if(!response.ok)throw new Error(data.error??'Не удалось сохранить настройки');
   saved=data.default_repository_password_mode as Mode;
   notice='Настройка сохранена. Она будет применяться к новым хранилищам.';
  }catch(e){error=e instanceof Error?e.message:'Ошибка сохранения';}
  finally{busy=false;}
 }
 onMount(()=>{void load();});
</script>
<section class="settings-workspace">
 <div class="heading"><div><span class="eyebrow">НАСТРОЙКИ СИСТЕМЫ</span><h1>Настройки BackupHub</h1><p class="secondary">Параметры этого экземпляра сервера.</p></div></div>
 <section class="detail-card">
  <h2>Хранение резервных копий</h2>
  <p class="secondary">Как создавать новые репозитории Restic по умолчанию?</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {#if ready}
   <form onsubmit={(e)=>{e.preventDefault();void save();}}>
    <label for="default-password-mode">Режим новых хранилищ</label>
    <select id="default-password-mode" bind:value={selected} disabled={busy}>
     <option value="password">С паролем — нужен ключ для аварийного восстановления</option>
     <option value="no_password">Без пароля — достаточно диска с архивом</option>
    </select>
    {#if selected==='no_password'}
     <p class="secondary">Любой получивший доступ к репозиторию сможет открыть его стандартной командой Restic. Отдельный файл ключа не потребуется.</p>
    {:else}
     <p class="secondary">BackupHub создаёт отдельный секретный ключ для каждого репозитория. Для восстановления после гибели сервера нужно заранее сохранить его копию.</p>
    {/if}
    <p class="secondary">Изменение касается только <strong>новых хранилищ</strong>. Режим уже созданных архивов не меняется. При создании нового хранилища можно выбрать другой режим.</p>
    <button type="submit" disabled={busy||saved===selected}>{busy?'Сохранение…':'Сохранить'}</button>
   </form>
  {:else}
   <button class="outline" onclick={()=>void load()} disabled={busy}>Повторить загрузку</button>
  {/if}
 </section>
</section>
<style>
 .settings-workspace{display:grid;gap:18px}
 form{display:grid;justify-items:start;gap:13px;max-width:720px}
 form label{font-weight:600}
 form select{max-width:100%;width:100%;background:#172338;color:#eee;border:1px solid #405474;border-radius:7px;padding:12px}
 form p{margin:0}
</style>
