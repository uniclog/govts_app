# Карта проекта Sonoryx

Карта описывает текущую рабочую копию проекта. Go-модуль — `uniclog.io/sonoryx`.
Описание продукта приведено в [README](README.md). Сценарии запуска,
ограничения и протокол перенесены в локальное
[техническое руководство](readme_docs/technical-guide.md).
Документы `readme_docs/` не входят в Git.

## Состав системы

| Часть | Где искать | Ответственность |
| --- | --- | --- |
| Сервер | `apps/server/`, `apps/server/internal/server/`, `apps/server/internal/voice/` | Запуск, конфигурация, сессии, каналы, доставка голосовых пакетов и событий |
| Идентичность и права | `internal/identity/`, `internal/auth/`, `apps/server/internal/persist/` | Ключи Ed25519, защищённое рукопожатие, SQLite-учётные записи и права |
| Клиентское ядро | `internal/clientcore/`, `internal/clientcore/internal/live/` | Жизненный цикл соединения, reconnect, состояние, UDP-запросы и обработка звука |
| Звук | `internal/audio/`, `apps/desktop/internal/nativeaudio/` | Общие PCM, Opus, RNNoise, VAD и voice gate. Десктопный адаптер открывает устройства malgo и захватывает звук через WASAPI |
| Экран | `apps/server/internal/media/`, `internal/mediasignal/`, `internal/clientcore/internal/screensignal/` | HTTPS signaling, WebRTC/SFU, учет потоков и подписок |
| Файлы | `apps/desktop/internal/filequeue/`, `internal/clientcore/internal/fileclient/`, `apps/server/internal/filetransfer/` | Локальная очередь и staging, файловый HTTPS, серверное хранилище вложений |
| Desktop UI | `apps/desktop/`, `apps/desktop/internal/wails/`, `apps/desktop/internal/desktopwindow/`, `apps/desktop/frontend/` | Wails 3, сервис для React/TypeScript, настройки и окна |
| Веб-шлюз | `apps/webgateway/`, `apps/webgateway/internal/` | HTTPS, веб-сессии, WebSocket и адаптер WebRTC/Opus ↔ PCM поверх фасада `clientcore` |
| Веб-клиент | `apps/web/`, `contracts/web/` | React-интерфейс для каналов, канального чата и звука, OpenAPI и схема WebSocket |
| Палитры | `shared-ui/theme-palettes.css` | Общие цветовые темы десктопного и веб-клиента |
| Обновления клиента | `apps/desktop/internal/clientupdate/`, `internal/updatemanifest/`, `cmd/update-sign/` | Подписанные GitHub Releases, загрузка, перезапуск и восстановление EXE |
| Общий контракт | `internal/domain/`, `internal/protocol/`, `internal/transport/udp/` | Модели состояния, бинарный UDP-протокол, граница сокета и кодека |

Фасад `internal/clientcore` доступен приложениям внутри репозитория. Пакеты
`apps/server/internal/`, `apps/desktop/internal/` и `apps/webgateway/internal/`
принадлежат своим приложениям.
`live`, `fileclient` и `screensignal` находятся в `internal/clientcore/internal/`
и доступны только внутри клиентского ядра.

## Точки входа и сборка

- `apps/server/main.go` разбирает флаги, выбирает пути и передаёт `RunConfig`
  в `apps/server/internal/server/runtime.go`. Пакет управляет UDP, SQLite, консолью,
  файловым HTTPS и медиа. `deploy.go` в этом пакете согласует активацию развёртывания.
  Версия сервера берётся из `version/release.json` через `internal/appversion/`.
- `apps/desktop/main.go` запускает Wails-приложение. Файлы
  `assets_development.go` и `assets_production.go` выбирают способ
  доставки frontend; production-сборка встраивает собранные ресурсы.
  Окно и трей принадлежат `apps/desktop/internal/desktopwindow/`.
  Версия клиента также хранится в `version/release.json`.
- `apps/webgateway/main.go` запускает HTTPS-шлюз. Режим входа выбирается в
  конфигурации. Опция `-add-account` создаёт доступ для режима приглашений. `npm run build:web`
  собирает `apps/web/dist`, который шлюз отдаёт с того же источника, что и API.
  [Сборка и настройка](apps/webgateway/README.md).
- `cmd/update-sign/` подписывает desktop EXE и манифест релиза, а с `-packed`
  сжимает EXE в zstd (`SRX64.exe.zst`) и добавляет его размер и SHA-256 в
  манифест `SRX64.exe.signature`. С `-legacy-out` дополнительно пишет манифест
  `signature` для имени `GTS64.exe` — по нему обновляются клиенты, выпущенные
  до переименования в Sonoryx. Закрытый seed
  получает из `UPDATE_SIGNING_KEY`; ключ не хранится в Git. Открытый ключ
  `apps/desktop/update-public-key.txt` встроен через `update_key.go`.
- `cmd/release-version/` проверяет соответствие тега версии клиента и
  создаёт `build/config.generated.yml` из шаблона `build/config.yml`, подставляя
  `clientVersion` из `version/release.json`. Исходный YAML не изменяется,
  сгенерированный файл исключён из Git. Локальные сборки не увеличивают версии.
- `version/release.json` содержит `clientVersion`, `serverVersion` и
  `minServerVersion`; `version/release.go` встраивает файл в бинарники.
  Параметры редактируются перед релизом и коммитятся до создания тега.
- `Taskfile.yml` связывает генерацию Wails bindings, сборку frontend,
  режим разработки и сборку desktop. Корневой `package.json` содержит
  npm-обёртки; зависимости и скрипты самого UI находятся в
  `apps/desktop/frontend/package.json`.
- `configs/server.example.json` — пример первичной конфигурации сервера. Без
  `-config` сервер создаёт встроенный канал `default`; JSON-конфигурация
  задаёт полное дерево каналов при первом импорте в SQLite. Затем сервер
  загружает имя, каналы и пороги из БД.

Отдельного исполняемого консольного голосового клиента в текущем дереве нет.
Низкоуровневые клиентские команды в `internal/clientcore/internal/live/request.go` остаются
частью пакета, но точка входа `cmd/client2` удалена.

## Голос и состояние

```text
React UI
  -> Wails service -> clientcore -> live -> transport/udp -> protocol
  -> voice Hub (сервер) -> UDP-получатели того же канала
```

1. `apps/desktop/internal/wails/service.go` принимает команды UI и переводит их в
   операции `clientcore`. DTO и проверки входных данных расположены в
   `apps/desktop/internal/wails/dto.go`; события для WebView — в `event_bridge.go`.
2. `internal/clientcore/app.go`, `session.go`, `supervisor.go` и
   `reconnect.go` управляют соединением, параллельными циклами, остановкой
   и повторным подключением без зависимости от Wails.
3. `internal/clientcore/internal/live/` выполняет защищённый handshake, контрольные запросы, heartbeat,
   прием пакетов, обновление состояния и сбор статистики. `internal/protocol/`
   описывает формат пакетов и полезных нагрузок;
   `internal/transport/udp/` читает/пишет датаграммы.
4. `apps/server/internal/voice/hub.go` — источник истины о сессиях, каналах,
   участниках, потоках экрана и ревизии состояния. `control.go`,
   `delivery.go`, `events.go`, `snapshot.go` и `screen.go` обслуживают
   соответствующие операции. `apps/server/internal/server/config.go` загружает
   стартовое дерево, `persistent.go` сохраняет/восстанавливает его из БД,
   `pipeline.go` — фоновые процессы сервера. `apps/server/internal/persist/` хранит
   учётные записи, баны, права, аудит и каналы.
5. Клиент получает snapshot и затем live-события; при расхождении ревизий
   запрашивает снимок заново. Для control-пакетов используется `RequestID`,
   на сервере повторы обрабатывает `voice.RequestCache`.

Аудиотракт: `malgo` захватывает PCM → опционально применяются RNNoise и
VAD/voice gate → Opus-кодер формирует голосовой UDP-пакет → сервер пересылает
его участникам канала, не декодируя → клиентский jitter buffer, Opus-декодер
и микшер передают звук в `malgo`-плеер. Основная сборка трактов находится в
`internal/clientcore/session.go`. Ядро получает PCM через фабрику сессионных
потоков (`internal/clientcore/audio.go`, `internal/audio/session.go`).
Выбор устройства, горячее переключение, мониторинг и предпрослушивание
принадлежат `apps/desktop/internal/nativeaudio/`. Предпрослушивание микрофона
переиспользует обработку PCM из `internal/clientcore/preview.go`.
Кодеки находятся в `internal/audio/`, а приём, jitter, декодирование
и микшер — в `internal/clientcore/internal/live/`.
Индивидуальная громкость и mute участников применяются на стороне клиента.

UDP-заголовок определён в `internal/protocol/packet.go`: 1 байт типа,
8 байт `SessionID`, по 4 байта `Sequence` и `RequestID`. Итого 17 байт
заголовка и до 1200 байт payload; защищённый datagram — до 1250 байт на
проводе. Типы охватывают аутентификацию, голос, heartbeat, смену канала,
snapshot, события, media credentials, модерацию и чат. После рукопожатия
голосовой/control UDP шифруется AES-GCM; старый анонимный `Hello` отклоняется.

## Текстовые сообщения

- `internal/domain/chat.go` задаёт адресатов, сообщения, диалоги и страницы.
- `internal/protocol/chat.go` кодирует команды отправки, истории, диалогов и
  прочтения. Страницы помещаются в прежний лимит payload 1200 байт; текст — до
  1000 байт UTF-8. Snapshot (с v6) и state events v2 передают постоянный `user_id`;
  snapshot v7 добавил флаг away участника.
- `apps/server/internal/persist/chat.go` выполняет миграцию SQLite до версии 4, сохраняет
  сообщения и позиции прочтения, выбирает историю по ID. Уникальная пара
  `(sender_id, client_id)` устраняет дубли после reconnect и перезапуска сервера.
- `apps/server/internal/voice/chat.go` обслуживает отдельную очередь на 128 запросов с
  пределом 4 ожидающих запросов на пользователя. SQLite работает вне голосового
  цикла; доступ проверяется перед запросом и перед возвратом результата.
- `internal/clientcore/internal/live/chat.go`, `internal/clientcore/chat.go` и
  `apps/desktop/internal/wails/service_chat.go` связывают защищённый UDP с Wails DTO.
- `apps/desktop/frontend/src/features/chat/` содержит интерфейс и ограниченный
  кеш истории, черновиков и отправок. Подтверждения объединяются по ID сообщения;
  `ChatChanged` ускоряет сверку, опрос раз в 5 секунд восстанавливает потери.
  Контекст включает endpoint, публичные ключи сервера/клиента и `user_id`.

## Файловые передачи

- `internal/clientcore/files.go` связывает файловый доступ с активной UDP-сессией.
  `internal/clientcore/internal/fileclient/` выполняет HTTPS-запросы, проверяет
  отпечаток TLS и управляет бюджетом запросов и повторами.
- `apps/desktop/internal/filequeue/` хранит задачи в `Sonoryx/file-queue.json`
  и обрабатывает временные файлы в `Sonoryx/file-staging`. Очередь загружается
  при первом использовании. Задача привязана к контексту сервера и аккаунта.
- `apps/server/internal/server/files.go` запускает файловый HTTPS.
  `apps/server/internal/filetransfer/` проверяет доступ через операции
  `voice.Hub` и хранит данные через операции `persist.Store`.
  Общие DTO и лимит чанка находятся в `internal/fileapi/`.

## Администрирование

- `apps/server/internal/persist/store.go` хранит биты kick/ban/drag в `users.permissions`
  и право admin в `users.admin` (миграция схемы до версии 5); `AccountsPage`
  и `AuditPage` читают аккаунты и журнал страницами. `DeleteAccountBy`
  (схема 6, `users.deleted`) оставляет скрытую запись для сообщений в каналах,
  заменяет ключ случайным, сбрасывает права и удаляет личную переписку; старый
  ключ при следующем входе регистрирует новый аккаунт.
- `apps/server/internal/voice/admin.go` — `Administration`: общие проверки иерархии,
  запись в SQLite с аудитом, применение к `Hub` и уведомление клиентов о
  правах. Его используют и локальная консоль (`apps/server/internal/server/console_accounts.go`,
  команды `account set-permission … admin`, `audit`), и `HandleAdmin`
  (`apps/server/internal/voice/admin_packet.go`) для пакета `PacketAdminRequest`.
- `internal/protocol/admin.go` кодирует запросы и ответы администрирования и
  биты байта привилегий (`PrivilegeAdmin`, `PrivilegeOwner`).
- `internal/clientcore/internal/live/admin.go`, `internal/clientcore/app.go` (`Administer`) и
  `apps/desktop/internal/wails/service_admin.go` связывают защищённый UDP с Wails DTO.
- `apps/server/internal/voice/channel_admin.go` — создание, правка, перемещение и удаление
  каналов в `Hub` (`PrepareChannel`/`AddChannel`, `CheckChannelUpdate`/`UpdateChannel`,
  `CheckChannelDelete`/`DeleteChannel`) с событиями `ChannelUpdated` и
  `ChannelDeleted`; `persist.InsertChannelBy`/`UpdateChannelBy`/`DeleteChannelBy`
  сохраняют их с аудитом. Клиент применяет события в `internal/clientcore/internal/live/live.go`.
- `apps/desktop/frontend/src/features/admin/AdminPanel.tsx` — окно
  «Администрирование» (аккаунты, дерево каналов с созданием, настройками,
  перемещением и удалением, журнал); правила и порядок дерева — в `adminRules.ts`; кнопка видна при
  `canAdmin` в `ClientViewDTO`.

## Демонстрация экрана

```text
React screenMedia / ScreenViews
  -> Wails service -> clientcore -> screensignal -> HTTPS signaling
  -> media.HTTPHandler -> media.Manager / Pion WebRTC SFU
  -> подписчики выбранного потока
```

- `apps/desktop/frontend/src/features/screen/` содержит захват экрана,
  публикацию, просмотр, профили и статистику. Окно просмотра открывается
  отдельно; список доступных потоков приходит в состоянии сервера.
- `internal/mediasignal/types.go` задаёт JSON-контракт. Серверный
  `apps/server/internal/media/http.go` обслуживает `/media/publish`,
  `/media/subscribe`, `/media/stop` и `/media/unsubscribe`. Запросы
  авторизуются credential, выданным активной голосовой сессии.
- `apps/server/internal/media/manager.go` управляет publisher/subscriber и сверяет
  доступ с состоянием `voice.Hub`; остальные файлы `apps/server/internal/media/`
  реализуют WebRTC, RTP relay/recovery, метрики и TLS identity. Сам экран
  передаётся через WebRTC, а не через голосовой UDP-протокол.
- `internal/clientcore/media.go` выдаёт доступ на время конкретной голосовой
  сессии. Клиент из `internal/clientcore/internal/screensignal/` выполняет HTTPS
  и проверяет отпечаток. Подтверждение ключа обрабатывает `service_media.go` в
  Wails-адаптере, pin хранится в `apps/desktop/internal/settings/`.

По умолчанию сервер слушает голосовой UDP-порт `9000`, HTTPS signaling на
`9002` и использует UDP `20000–20100` для WebRTC. Флаги
`-port`, `-media-port`, `-media-min-port`, `-media-max-port`,
`-media-advertised-ip` и `-media-identity` меняют эту конфигурацию;
`-media-port 0` отключает демонстрацию экрана. Для публичного адреса
WebRTC требуется корректный `-media-advertised-ip`.

## Desktop UI и локальные данные

- `apps/desktop/frontend/src/App.tsx` собирает экран приложения и
  синхронизирует UI с backend. `api.ts` — вызовы Wails и клиентские DTO,
  `model.ts` — преобразования состояния. Дерево каналов выделено в
  `features/channels/`, настройки и предпрослушивание UI — в `features/settings/`.
- `features/connection/` — подключение, статус и статистика соединения;
  `apps/desktop/internal/serverstatus/` — UDP-опрос и кэш статусов.
  `features/participants/` — строка участника;
  `features/screen/` — демонстрация и просмотр экрана. Стили — в
  `src/*.css`.
- `features/updates/` — кнопка доступного обновления в шапке и модальное окно
  загрузки/установки. Раздела обновлений на странице настроек нет.
- Сгенерированные Wails bindings находятся в
  `apps/desktop/frontend/bindings/uniclog.io/sonoryx/`. Их следует
  пересоздавать после изменения публичных методов или DTO Wails-сервиса,
  а не редактировать вручную.
- `apps/desktop/internal/settings/store.go` сохраняет версионированные настройки
  в каталоге конфигурации пользователя `Sonoryx/settings.json`: имя,
  аудиоустройства, параметры шумоподавления/VAD, тему, звуковой пак и
  громкость сигналов, выключение микрофона при «Отошёл» и доверенные
  media-ключи. Идентификаторы паков задаёт `clientsettings`, синтез сигналов —
  `internal/clientcore/internal/live/sounds.go`. Адрес сервера UI хранит в browser localStorage. Отдельный
  `Sonoryx/client.seed` хранит приватный seed пользователя, а
  `Sonoryx/voice-pins.json` — доверенные голосовые серверы (TOFU).
  `legacy.go` при запуске копирует эти три файла из старого профиля `Govts/`,
  если в `Sonoryx/` их ещё нет; старый каталог не меняется.
- `apps/desktop/internal/clientupdate/service.go` связывает UI с Wails updater, планирует
  проверки. `provider.go`
  выбирает стабильный Windows amd64 asset и обязательно проверяет подписанный
  манифест; если в манифесте и релизе есть сжатый `SRX64.exe.zst`, качает его
  и распаковывает на лету, иначе — обычный `SRX64.exe`.
  `internal/updatemanifest/` задаёт формат и Ed25519-проверку.
- `apps/desktop/internal/clientupdate/recovery.go` сохраняет прежний EXE и следит за
  подтверждением запуска нового интерфейса с пределом 90 секунд. Windows API
  запуска и проверки процессов — в `process_windows.go`. Helper и watcher
  обрабатываются до логирования и SingleInstance; PID нового обычного клиента
  записывается после SingleInstance. Профиль при откате EXE не откатывается.
- `Sonoryx/pending-update.json` и `Sonoryx/update-recovery/` — план и копии
  восстановления; `Sonoryx/update-recovery-result.txt` — результат отката.
  При запуске клиент подтверждает и план в `Govts/pending-update.json`: его
  оставляет watcher клиента, выпущенного до переименования, и без подтверждения
  он откатил бы обновление.
  Черновики чата перед обновлением сохраняются в frontend `localStorage`.

## Проверки, версии и релизы

- Go-тесты находятся рядом с пакетами (`*_test.go`); frontend-тесты — в
  `apps/desktop/frontend/src/`. Основные локальные проверки:
  `go test ./...`, `go vet ./...`, `npm test --prefix apps/desktop/frontend`
  и `npm run build --prefix apps/desktop/frontend`.
- `.github/workflows/release.yml` срабатывает на тег `vX.Y.Z`, сверяет
  его с `clientVersion` в `version/release.json` и собирает Windows desktop и Linux
  server в отдельных заданиях. Подпись desktop через secret `UPDATE_SIGNING_KEY`
  выполняется после обеих сборок. Отдельное задание загружает
  бинарники, `SRX64.exe.zst`, `SRX64.exe.signature`, копию `GTS64.exe` с
  `signature` для старых клиентов и `SHA256SUMS` в draft и затем публикует релиз.
  Уже опубликованный релиз под тем же тегом не заменяется.
- `internal/appversion/` разбирает и сравнивает версии, проверяет параметры
  релиза из встроенного `version/release.json`. Минимальная версия сервера
  используется при подключении клиента и включается в подписанный манифест.
  Порог поддержки голосовых пакетов задаётся отдельно от требований релиза.
- `scripts/build-server.ps1`, `deploy-server.ps1` и
  `redeploy-server.sh` — локальные сценарии сборки/развёртывания сервера;
  они не являются частью GitHub Release workflow.

При поиске конкретного изменения сначала выбирайте тракт — голос/состояние,
экран или UI — и меняйте контракт на его границе вместе с соответствующими
тестами. Ограничения проекта и команды запуска поддерживаются в техническом руководстве,
чтобы эта карта оставалась навигацией по коду, а не дублировала руководство.
