<script lang="ts">
  import { onMount } from 'svelte';
  import BackupPane from './BackupPane.svelte';
  import NavIcon from './NavIcon.svelte';
  import SettingsPane from './SettingsPane.svelte';
  import Workload from './Workload.svelte';

  type User = { id: string; username: string; role: string };
  type SetupStatus = { setup_required: boolean };
  type Health = { service: string; version: string; backups_available: boolean; timestamp: string };
  type Identity = { instance_uuid: string; schema_version: number; production_ready: boolean };
  type AuditEvent = { id: number; at: string; actor: string; action: string; resource: string };
  type AuditList = { events: AuditEvent[] };
  type View = 'loading' | 'bootstrap' | 'login' | 'dashboard' | 'error';

  let view = $state<View>('loading');
  type Section='overview'|'jobs'|'sources'|'storages'|'history'|'runs';
  type Tab=Section|'audit'|'settings'|'workload';
  let activeTab=$state<Tab>('overview');
  const navItems=[
   {id:'overview',label:'Обзор',icon:'overview',group:'main'},
   {id:'jobs',label:'Задания',icon:'jobs',group:'main'},
   {id:'sources',label:'Источники',icon:'sources',group:'main'},
   {id:'storages',label:'Хранилища',icon:'storages',group:'main'},
   {id:'history',label:'История копий',icon:'history',group:'history'},
   {id:'runs',label:'Журнал выполнения',icon:'runs',group:'history'},
   {id:'workload',label:'Расписание и загрузка',icon:'schedules',group:'history'},
   {id:'audit',label:'События безопасности',icon:'audit',group:'system'},
   {id:'settings',label:'Настройки',icon:'settings',group:'system'}
  ] as const;
  function navigate(tab:Section){activeTab=tab;}
  let busy = $state(false);
  let errorMessage = $state('');
  let notice = $state('');
  let username = $state('admin');
  let password = $state('');
  let bootstrapToken = $state('');
  let user = $state<User | null>(null);
  let health = $state<Health | null>(null);
  let identity = $state<Identity | null>(null);
  let events = $state<AuditEvent[]>([]);

  const roleName: Record<string, string> = {
    administrator: 'Администратор', backup_operator: 'Оператор копирования',
    restore_operator: 'Оператор восстановления', auditor: 'Аудитор', viewer: 'Наблюдатель'
  };
  let canAudit = $derived(user?.role === 'administrator' || user?.role === 'auditor');
  let isAdmin = $derived(user?.role === 'administrator');

  function describeError(error: unknown): string {
    if (error instanceof Error) {
      if (error.message === 'invalid credentials') return 'Неверные учётные данные или истёк срок действия токена.';
      if (error.message === 'Failed to fetch') return 'Нет соединения с BackupHub. Проверьте адрес и настройки HTTPS.';
      return error.message;
    }
    return 'Неизвестная ошибка';
  }
  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const res = await fetch('/api/v1' + path, { ...init, credentials: 'same-origin' });
    const body: unknown = await res.json().catch(() => null);
    if (!res.ok) {
      const detail = body && typeof body === 'object' && 'error' in body ? String((body as {error: unknown}).error) : `HTTP ${res.status}`;
      throw new Error(detail);
    }
    return body as T;
  }
  function csrfCookie(): string {
    const cookie = document.cookie.split('; ').find((value) => value.startsWith('backuphub_csrf='));
    return cookie ? decodeURIComponent(cookie.slice('backuphub_csrf='.length)) : '';
  }
  function localDate(input: string): string {
    const parsed = new Date(input);
    return Number.isNaN(parsed.getTime()) ? input : parsed.toLocaleString('ru-RU');
  }
  async function initialize() {
    busy = true; errorMessage = '';
    try {
      const status = await request<SetupStatus>('/setup/status');
      if (status.setup_required) { view = 'bootstrap'; return; }
      try {
        user = await request<User>('/auth/me');
        view = 'dashboard';
        await refreshDashboard();
      } catch {
        user = null; view = 'login';
      }
    } catch (err) { errorMessage = describeError(err); view = 'error'; }
    finally { busy = false; }
  }
  async function createAdministrator() {
    busy = true; errorMessage = '';
    try {
      await request<User>('/setup/admin', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({bootstrap_token: bootstrapToken, username, password})
      });
      bootstrapToken = ''; password = '';
      notice = 'Администратор создан. Войдите с выбранным логином и паролем.';
      view = 'login';
    } catch (err) { errorMessage = describeError(err); }
    finally { busy = false; }
  }
  async function login() {
    busy = true; errorMessage = ''; notice = '';
    try {
      const response = await request<{user: User}>('/auth/login', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({username, password})
      });
      user = response.user; password = ''; view = 'dashboard';
      await refreshDashboard();
    } catch (err) { errorMessage = describeError(err); }
    finally { busy = false; }
  }
  async function refreshDashboard() {
    try {
      health = await request<Health>('/health');
      identity = await request<Identity>('/system/identity');
      if (canAudit) {
        const response = await request<AuditList>('/audit?limit=20');
        events = response.events;
      }
    } catch (err) { errorMessage = describeError(err); }
  }
  async function logout() {
    busy = true; errorMessage = '';
    try {
      await fetch('/api/v1/auth/logout', {
        method: 'POST', credentials: 'same-origin', headers: { 'X-BackupHub-CSRF': csrfCookie() }
      }).then((response) => { if (!response.ok) throw new Error(`HTTP ${response.status}`); });
      user = null; identity = null; events = []; view = 'login';
    } catch (err) { errorMessage = describeError(err); }
    finally { busy = false; }
  }
  onMount(() => { void initialize(); });
</script>

{#if view === 'loading'}
  <main class="center-screen"><div class="loading-card"><div class="brand-icon">B</div><h1>BackupHub</h1><p>Подключение к серверу…</p></div></main>
{:else if view === 'error'}
  <main class="center-screen"><section class="auth-card"><div class="brand-icon">B</div><h1>Не удалось подключиться</h1><p class="error" role="alert">{errorMessage}</p><button onclick={() => void initialize()} disabled={busy}>Повторить попытку</button></section></main>
{:else if view === 'bootstrap' || view === 'login'}
  <main class="center-screen">
    <section class="auth-card">
      <div class="brandline"><div class="brand-icon">B</div><div><span class="wordmark">BackupHub</span><span class="brand-caption">Система резервного копирования</span></div></div>
      {#if view === 'bootstrap'}
        <span class="eyebrow">ПЕРВОНАЧАЛЬНАЯ НАСТРОЙКА</span>
        <h1>Создание администратора</h1>
        <p class="secondary">На этом сервере ещё нет администратора. Выполните <code>backuphub bootstrap</code> и введите полученный одноразовый токен. Если администратор уже создавался, проверьте каталог данных запущенного сервиса.</p>
        <form onsubmit={(event) => { event.preventDefault(); void createAdministrator(); }}>
          <label for="bootstrap-token">Одноразовый токен</label>
          <input id="bootstrap-token" type="password" bind:value={bootstrapToken} autocomplete="off" required placeholder="Токен с сервера" />
          <label for="setup-user">Имя администратора</label>
          <input id="setup-user" bind:value={username} autocomplete="username" minlength="3" maxlength="64" required />
          <label for="setup-password">Пароль</label>
          <input id="setup-password" type="password" bind:value={password} autocomplete="new-password" minlength="8" required placeholder="Не менее 8 символов" />
          {#if errorMessage}<p class="error" role="alert">{errorMessage}</p>{/if}
          <button type="submit" disabled={busy}>{busy ? 'Сохранение…' : 'Создать администратора'}</button>
        </form>
      {:else}
        <span class="eyebrow">УПРАВЛЕНИЕ РЕЗЕРВНЫМИ КОПИЯМИ</span>
        <h1>Вход в систему</h1>
        <p class="secondary">Используйте учётную запись администратора BackupHub.</p>
        <form onsubmit={(event) => { event.preventDefault(); void login(); }}>
          <label for="login-user">Имя пользователя</label>
          <input id="login-user" bind:value={username} autocomplete="username" required />
          <label for="login-password">Пароль</label>
          <input id="login-password" type="password" bind:value={password} autocomplete="current-password" required />
          {#if notice}<p class="notice" role="status">{notice}</p>{/if}
          {#if errorMessage}<p class="error" role="alert">{errorMessage}</p>{/if}
          <button type="submit" disabled={busy}>{busy ? 'Проверка…' : 'Войти'}</button>
        </form>
      {/if}

    </section>
  </main>
{:else}
  <div class="shell">
   <aside class="sidebar">
    <div class="brandline"><div class="brand-icon"><span class="logo-mark">B</span></div><div><span class="wordmark">BackupHub</span><span class="brand-caption">Центр резервного копирования</span></div></div>
    <div class="sidebar-navigation">
     {#each ['main','history','system'] as group}
      <p class="side-section">{group==='main'?'УПРАВЛЕНИЕ':group==='history'?'МОНИТОРИНГ':'СИСТЕМА'}</p>
      {#each navItems.filter(item=>item.group===group && (item.id!=='audit'||canAudit) && (item.id!=='settings'||isAdmin)) as item (item.id)}
       <button class="nav-button" class:active={activeTab===item.id} aria-label={item.label} aria-current={activeTab===item.id?'page':undefined} onclick={()=>activeTab=item.id}><NavIcon name={item.icon} size={18}/><span>{item.label}</span></button>
      {/each}
     {/each}
    </div>
    <div class="sidebar-bottom"><span class="side-status-dot"></span><span>{health?.service==='running'?'Сервер доступен':'Состояние неизвестно'}</span></div>
   </aside>
   <div class="content-shell">
    <header class="topbar"><div class="breadcrumbs">BackupHub <span>/</span> {navItems.find(i=>i.id===activeTab)?.label??'Обзор'}</div><div class="topbar-user"><div class="user-avatar">{user?.username.slice(0,1).toUpperCase()}</div><div class="user-title"><strong>{user?.username}</strong><small>{roleName[user?.role??'']??user?.role}</small></div><button class="logout-button" title="Выйти" aria-label="Выйти" onclick={()=>void logout()} disabled={busy}><NavIcon name="logout"/></button></div></header>
    <main class="dashboard">
     {#if errorMessage}<div class="error" role="alert">{errorMessage}</div>{/if}
     {#if activeTab==='settings' && isAdmin}
      <SettingsPane/>
     {:else if activeTab==='workload'}
      <Workload/>
     {:else if activeTab==='audit' && canAudit}
      <div class="workspace-header"><div><span class="eyebrow">БЕЗОПАСНОСТЬ</span><h1>События безопасности</h1><p class="secondary">Изменения конфигурации и действия пользователей.</p></div><button class="outline" onclick={()=>void refreshDashboard()}>Обновить</button></div>
      <section class="detail-card">
       {#if events.length===0}<p class="secondary">Пока нет событий.</p>{:else}<div class="table-scroll"><table><thead><tr><th>Время</th><th>Действие</th><th>Объект</th><th>Пользователь</th></tr></thead><tbody>{#each events as event(event.id)}<tr><td>{localDate(event.at)}</td><td>{event.action}</td><td>{event.resource}</td><td><code>{event.actor.slice(0,12)}</code></td></tr>{/each}</tbody></table></div>{/if}
      </section>
     {:else}
      <BackupPane section={activeTab as Section} role={user?.role??''} onNavigate={navigate}/>
     {/if}
    </main>
   </div>
  </div>
{/if}
