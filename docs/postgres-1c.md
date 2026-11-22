# Копирование Postgres Pro для 1С

Профиль «Postgres Pro для 1С» рассчитан на отдельную информационную базу с обычной таблицей `public.config` и полем `binarydata bytea`. Текстовый вывод крупных значений `bytea` может значительно увеличивать размер отдельного значения и завершаться ошибкой выделения памяти. Размер таблицы сам по себе такого ограничения не определяет.

BackupHub сохраняет остальную базу как архив `database.dump`. В архив входит определение `public.config` и связанные объекты. Данные таблицы сохраняются отдельным потоком `COPY ... WITH (FORMAT binary)` в `config.copy`. Обе операции читают одну экспортированную MVCC-точку. Перед публикацией BackupHub проверяет структуру архива, двоичный поток и сохраняет SHA-256 файлов в `backuphub-postgres-1c.json`. Во время копирования не обновляйте конфигурацию 1С и не запускайте миграцию схемы.

Используйте клиенты `psql`, `pg_dump` и `pg_restore` из совместимого выпуска. Для восстановления выбирайте изолированный сервер той же редакции и основной версии, с совместимыми кодировкой, локалью и версиями расширений. Владелец и ACL исходной базы не переносятся этим профилем. Нужные роли и права создайте на восстановительном стенде отдельно.

## Восстановление в новую базу

Восстановите все файлы снимка в новый каталог BackupHub. Скопируйте их в один рабочий каталог на стенде. Из этого каталога проверьте контрольные суммы потоково.

```sh
python3 - <<'PY'
import hashlib
import json
from pathlib import Path

manifest = json.loads(Path("backuphub-postgres-1c.json").read_text())
if manifest["format"] != "backuphub.postgres-1c.v1":
    raise SystemExit("Unsupported backup format")
for name in ("database.dump", "config.copy"):
    expected = manifest["files"][name]
    with open(name, "rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    if digest != expected["sha256"] or Path(name).stat().st_size != expected["bytes"]:
        raise SystemExit("Checksum or size mismatch: " + name)
print(manifest["source"])
PY
```

Проверьте совпадение версии и локали с полем `source` в манифесте. Задайте параметры подключения к стенду через обычные параметры libpq. Используйте парольный файл `PGPASSFILE` с правами `0600` либо локальную аутентификацию.

```sh
set -eu
target_db=backuphub_1c_restore
createdb --maintenance-db=postgres --template=template0 --encoding=UTF8 \
  --lc-collate=ru_RU.UTF-8 --lc-ctype=ru_RU.UTF-8 "$target_db"
pg_restore --exit-on-error --no-owner --no-acl --section=pre-data -d "$target_db" database.dump
pg_restore --exit-on-error --no-owner --no-acl --section=data -d "$target_db" database.dump
psql -X -v ON_ERROR_STOP=1 -d "$target_db" \
  -c 'COPY public.config FROM STDIN WITH (FORMAT binary)' < config.copy
pg_restore --exit-on-error --no-owner --no-acl --section=post-data -d "$target_db" database.dump
```

В строке `createdb` замените кодировку и параметры локали значениями исходной базы. Пример использует провайдер libc. Для базы с ICU или встроенным провайдером укажите соответствующий `--locale-provider` и локаль провайдера из манифеста. Если стадия завершилась ошибкой, выясните причину, а затем повторите восстановление в другой чистой базе. После копирования проверьте количество строк и контрольные данные в `public.config`, индексы и ограничения. Подключите отдельную тестовую информационную базу 1С и выполните контрольное открытие и прикладную операцию.

Для физической копии всего кластера Postgres Pro с восстановлением на момент времени используйте отдельный регламент штатного `pg_probackup` с архивом WAL.

- [Экспорт и импорт единой точки чтения](https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-SNAPSHOT-SYNCHRONIZATION)
- [Форматы и совместимость COPY](https://www.postgresql.org/docs/17/sql-copy.html)
- [Настройка Postgres Pro для решений 1С](https://postgrespro.ru/docs/enterprise/17/config-one-c)
- [Физическое резервное копирование pg_probackup](https://postgrespro.ru/products/extensions/pg_probackup)
