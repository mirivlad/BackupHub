# Архитектура BackupHub

Источник требований: [спецификация v0.1](specification-v0.1.md). Описанное ниже разграничивает **текущий код** и целевое состояние.

## Компоненты и границы

```text
Браузер (Svelte/TypeScript — запланирован)
           │ HTTPS (Nginx либо встроенный TLS)
           ▼
BackupHub: REST API / RBAC / audit / export / restore
           │
           ├── Auth + bootstrap + sessions (реализован базовый контур)
           ├── Vault AES-256-GCM + внешний master key (реализован)
           ├── Scheduler + orchestrator (в разработке)
           │       └── Job state machine (реализованы контракты)
           ├── Connector registry (контракты)
           │       ├── SMB2/3 (SMB1 — изолирован)
           │       ├── SSH/SFTP
           │       ├── Windows / WinRM / VSS
           │       ├── PostgreSQL / PostgreSQL Pro 1C
           │       └── Dovecot / IMAP / Thunderbird
           ├── Snapshot catalog + search (SQLite/WAL)
           ├── Capture / restore strategies + portable manifest (контракты)
           └── Storage adapter — Restic preferred (контракт)
                   └── Primary repo + independent secondary repo
```

## Код

- `internal/domain` — переносимые типы снимка, проверка комплектности, состояния и чистый dry-run ротации.
- `internal/catalog` — SQLite/WAL, транзакционные миграции до v2, постоянный instance UUID. Каталог должен впоследствии восстанавливаться из репозитория.
- `internal/auth` — Argon2id, локальный bootstrap администратора, сессии, базовый аудит.
- `internal/vault` — AES-256-GCM, ключ вне БД, отказ при утере ключа или повреждении данных.
- `internal/connectors` — интерфейсы обнаружения, проверки, копирования и восстановления; не скрипты.
- `internal/storage` — интерфейс транзакционного репозитория; Restic-реализация впереди.
- `internal/httpapi` — HTTP: health, setup/status, setup/admin, login/logout/me, защищённые identity и audit. Ещё **нет** API реальных backup/restore.
- `cmd/backuphub` — команды `init`, `bootstrap`, `serve`.

## Инварианты

1. Отсутствующая обязательная часть блокирует commit снимка.
2. Каталог восстанавливаем из переносимых манифестов; SQLite не должна быть единственным источником истины.
3. Источник никогда не получает прав на удаление/очистку резервных копий.
4. Retention — отдельная привилегированная операция с dry-run, защитой hold и последней проверенной копии.
5. Создание копии, проверка данных и проверенное восстановление — три разные оси качества.
6. Коннектор не объявляет app-consistent без доказанного штатного механизма.
7. Восстановление приложения требует отдельного совместимого restore recipe.
8. Ключи и пароли не записываются в обычные настройки и логи.
9. Никаких legacy-скриптов вместо нативных P0-коннекторов.
10. Автономность каждой площадки гарантирована `instance_uuid`; fleet-view только в будущем.

## Текущие ограничения

Auth/Vault — подготовка к безопасному подключению реальных источников, **не** разрешение запускать BackupHub в production. Нет управления всеми пользователями и scope-based authorization для источников. Нет внешнего recovery kit. Хранение master.key на том же хосте защищает от отдельной утечки SQLite, но не от полного взлома машины. Реальный backend Restic и его проверки ещё предстоит реализовать.

Сопровождающие тесты должны проверять не только успешные операции, но и удалённый ключ, повреждённый шифротекст, ошибочный bootstrap, доступ без сессии и запрещённое действие без CSRF.
