<script lang="ts">
  import { onMount } from 'svelte';
  import BackupPane from './BackupPane.svelte';
  import SettingsPane from './SettingsPane.svelte';
  import RecoveryPane from './RecoveryPane.svelte';

  type User = { id: string; username: string; role: string };
  type SetupStatus = { setup_required: boolean };
  type Health = { service: string; version: string; backups_available: boolean; timestamp: string };
  type Identity = { instance_uuid: string; schema_version: number; production_ready: boolean };
  type AuditEvent = { id: number; at: string; actor: string; action: string; resource: string };
  type AuditList = { events: AuditEvent[] };
  type View = 'loading' | 'bootstrap' | 'login' | 'dashboard' | 'error';

  let view = $state<View>('loading');
  let activeTab = $state<'overview' | 'audit' | 'backup' | 'settings' | 'recovery'>('overview');
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
        <p class="secondary">Выполните <code>backuphub bootstrap</code> на сервере и введите одноразовый токен. Токен действителен 20 минут.</p>
        <form onsubmit={(event) => { event.preventDefault(); void createAdministrator(); }}>
          <label for="bootstrap-token">Одноразовый токен</label>
          <input id="bootstrap-token" type="password" bind:value={bootstrapToken} autocomplete="off" required placeholder="Токен с сервера" />
          <label for="setup-user">Имя администратора</label>
          <input id="setup-user" bind:value={username} autocomplete="username" minlength="3" maxlength="64" required />
          <label for="setup-password">Пароль</label>
          <input id="setup-password" type="password" bind:value={password} autocomplete="new-password" minlength="12" required placeholder="Не менее 12 символов" />
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
      <p class="footnote">Один независимый BackupHub на площадку · GPL-3.0</p>
    </section>
  </main>
{:else}
  <div class="shell">
    <aside class="sidebar">
      <div class="brandline"><div class="brand-icon">B</div><div><span class="wordmark">BackupHub</span><span class="brand-caption">Резервное копирование</span></div></div>
      <div class="side-section">МОНИТОРИНГ</div>
      <button class:active={activeTab === 'overview'} class="nav-button" onclick={() => activeTab = 'overview'}>Обзор</button>
      {#if canAudit}<button class:active={activeTab === 'audit'} class="nav-button" onclick={() => activeTab = 'audit'}>События</button>{/if}
      <div class="side-section">УПРАВЛЕНИЕ</div>
      <button class:active={activeTab === 'backup'} class="nav-button" onclick={() => activeTab = 'backup'}>Хранилища, задания и история</button>
      {#if isAdmin}
        <button class:active={activeTab === 'settings'} class="nav-button" onclick={() => activeTab = 'settings'}>Настройки</button>
        <button class:active={activeTab === 'recovery'} class="nav-button" onclick={() => activeTab = 'recovery'}>Аварийное восстановление</button>
      {/if}
      <div class="sidebar-bottom">Предварительная версия · проверьте восстановление до использования</div>
    </aside>
    <div class="content-shell">
      <header class="topbar"><span class="topbar-title">{activeTab === 'overview' ? 'Обзор системы' : activeTab === 'backup' ? 'Источники и история' : activeTab === 'settings' ? 'Настройки' : activeTab === 'recovery' ? 'Аварийное восстановление' : 'Журнал безопасности'}</span><div class="topbar-user"><span>{user?.username} · {roleName[user?.role ?? ''] ?? user?.role}</span><button class="text-button" onclick={() => void logout()} disabled={busy}>Выйти</button></div></header>
      <main class="dashboard">
        {#if errorMessage}<p class="error" role="alert">{errorMessage}</p>{/if}
        {#if activeTab === 'overview'}
          <div class="heading"><div><span class="eyebrow">СОСТОЯНИЕ СИСТЕМЫ</span><h1>BackupHub</h1><p class="secondary">Сведения о данном автономном экземпляре.</p></div><button class="outline" onclick={() => void refreshDashboard()}>Обновить</button></div>
          <div class="warning"><strong>Ранняя функциональная версия.</strong><span>Основные файловые сценарии работают, но не заменяют проверенное восстановление приложений. После добавления источника обязательно проверьте реальный снимок и его экспорт.</span></div>
          <div class="stat-grid">
            <section class="stat-card"><span>Состояние API</span><strong>{health?.service === 'running' ? 'Работает' : 'Нет данных'}</strong><small>Версия {health?.version ?? '—'}</small></section>
            <section class="stat-card"><span>Готовность копирования</span><strong>{health?.backups_available ? 'Доступно' : 'Не готово'}</strong><small>Факт запуска не подтверждает восстановимость данных</small></section>
            <section class="stat-card"><span>Схема каталога</span><strong>{identity?.schema_version ?? '—'}</strong><small>SQLite / автономный экземпляр</small></section>
          </div>
          <section class="detail-card"><h2>Идентификатор экземпляра</h2><code class="instance-id">{identity?.instance_uuid ?? '—'}</code><p class="secondary">Используется для разграничения независимых площадок и будущего объединённого просмотра.</p></section>
          <section class="detail-card"><h2>Рабочие возможности</h2><p class="secondary">В разделе «Источники, история и восстановление» можно создавать задания, просматривать завершённые снимки, скачивать выбранные файлы и папки ZIP и восстанавливать в новый каталог на сервере. PostgreSQL 1С split-config, Active Directory, SMB1, email и полноценная ротация ещё не готовы.</p></section>
        {:else if activeTab === 'backup'}
          <BackupPane role={user?.role ?? ''} />
        {:else if activeTab === 'settings' && isAdmin}
          <SettingsPane />
        {:else if activeTab === 'recovery' && isAdmin}
          <RecoveryPane />
        {:else if canAudit}
          <div class="heading"><div><span class="eyebrow">БЕЗОПАСНОСТЬ</span><h1>Журнал событий</h1><p class="secondary">Последние 20 событий авторизации и администрирования.</p></div><button class="outline" onclick={() => void refreshDashboard()}>Обновить</button></div>
          <section class="detail-card">
            {#if events.length === 0}<p class="secondary">Записей пока нет.</p>{:else}
              <div class="table-scroll"><table><thead><tr><th>Дата</th><th>Действие</th><th>Объект</th><th>Идентификатор пользователя</th></tr></thead><tbody>{#each events as event (event.id)}<tr><td>{localDate(event.at)}</td><td><code>{event.action}</code></td><td>{event.resource}</td><td><code>{event.actor}</code></td></tr>{/each}</tbody></table></div>
            {/if}
          </section>
        {/if}
      </main>
    </div>
  </div>
{/if}
