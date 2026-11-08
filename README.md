# BackupHub

**BackupHub** — свободная система резервного копирования и **восстановления** для смешанных инфраструктур Linux, Windows, SMB/Samba, PostgreSQL/1С и почтовых серверов.

**Состояние:** ранняя разработка; рабочих коннекторов резервного копирования пока нет. **Не используйте этот код для защиты реальных данных.** Публичный релиз 1.0 возможен только после прохождения критериев приёмки.

## Принципы

- Восстановление важнее факта запуска копирования. Отдельные статусы создания, проверки целостности и проверенного восстановления.
- Agentless-first: штатные коннекторы SMB, SSH/SFTP, WinRM/VSS, PostgreSQL, Dovecot/IMAP.
- Не оболочка над Bash/cron. Скриптовый коннектор — отдельно после основной функциональности.
- Логически полные исторические снимки, переносимые манифесты, независимые хранилища, защищённые правила удержания.
- Восстановление и экспорт через веб-интерфейс файлов, каталогов, наборов и приложений по проверенным стратегиям.
- Один независимый экземпляр на площадку; объединённый просмотр позже.

## Архитектура

Модульный Go-монолит, SQLite/WAL, будущий Svelte/TypeScript, REST API и Restic за адаптером хранения. Ни Kubernetes, ни внешний брокер, ни постоянно работающие агенты не требуются.

## Разработка и безопасный локальный запуск

Go 1.27+:

```bash
go mod download
go test ./...
go build -o backuphub ./cmd/backuphub
mkdir -m 0700 -p ./local-state
./backuphub init -data-dir ./local-state
./backuphub bootstrap -data-dir ./local-state
./backuphub serve -data-dir ./local-state
curl http://127.0.0.1:8085/api/v1/health
```

Сервис слушает только loopback и отдаёт `backups_available: false` пока нет работающего engine. Создание первого администратора и дальнейший вход сейчас доступны через API (веб-интерфейс ещё не готов), см. [пошаговую настройку](docs/security-bootstrap.md).

**Важно:** храните `master.key` вне основного сервера в защищённом offline-комплекте. Не публикуйте порт Backend без TLS reverse proxy.

## Документация

- [ТЗ и спецификация v0.1](docs/specification-v0.1.md)
- [Архитектурные границы](docs/architecture.md)
- [Текущая настройка безопасности](docs/security-bootstrap.md)
- [Restic: переносимые снимки и ограничения](docs/restic-adapter.md)
- [План разработки](docs/roadmap.md)
- [ADR: лицензия](docs/adr/0001-license.md) и [ADR: архитектура](docs/adr/0002-core.md)

## Лицензия

**GNU General Public License v3.0 only** (`GPL-3.0-only`). См. [LICENSE](LICENSE). Copyright (C) 2026 BackupHub contributors.
