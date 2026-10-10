# Карта проекта Govts

Карта описывает текущую рабочую копию проекта. Go-модуль — `uniclog.io/govts`.
Описание продукта приведено в [README](README.md). Сценарии запуска,
ограничения и протокол перенесены в локальное
[техническое руководство](readme_docs/technical-guide.md).
Документы `readme_docs/` не входят в Git.

## Состав системы

| Часть | Где искать | Ответственность |
| --- | --- | --- |
| Сервер | `cmd/server/`, `internal/server/`, `internal/voice/` | Запуск, конфигурация, сессии, каналы, доставка голосовых пакетов и событий |
| Идентичность и права | `internal/identity/`, `internal/auth/`, `internal/persist/` | Ключи Ed25519, защищённое рукопожатие, SQLite-учётные записи и права |
| Клиентское ядро | `internal/clientapp/`, `internal/client/` | Жизненный цикл соединения, reconnect, состояние, UDP-запросы и обработка звука |
| Звук | `internal/audio/` | Устройства malgo, PCM, Opus, RNNoise, VAD, voice gate и воспроизведение |
| Экран | `internal/media/`, `internal/mediasignal/`, `internal/clientapp/media.go` | HTTPS signaling, WebRTC/SFU, учет потоков и подписок |
| Desktop UI | `cmd/desktop/`, `internal/ui/wails/`, `cmd/desktop/frontend/` | Wails 3, сервис для React/TypeScript, настройки и окна |
| Обновления клиента | `internal/clientupdate/`, `internal/updatemanifest/`, `cmd/update-sign/` | Подписанные GitHub Releases, загрузка, перезапуск и восстановление EXE |
| Общий контракт | `internal/domain/`, `internal/protocol/`, `internal/transport/udp/` | Модели состояния, бинарный UDP-протокол, граница сокета и кодека |

Пакеты `internal/` доступны только коду этого Go-модуля: публичного Go API
проект пока не предоставляет.

## Точки входа и сборка

- `cmd/server/main.go` запускает голосовой UDP-сервер, локальную консоль
  управления правами и, если не отключён, HTTPS-сервер сигнализации и WebRTC SFU.
  Версию сервера получает из `version/release.json` через `internal/appversion/`.
- `cmd/desktop/main.go` запускает Wails-приложение. Файлы
  `assets_development.go` и `assets_production.go` выбирают способ
  доставки frontend; production-сборка встраивает собранные ресурсы.
  Версия клиента также хранится в `version/release.json`.
- `cmd/update-sign/` подписывает desktop EXE и манифест релиза, а с `-packed`
  сжимает EXE в zstd (`GTS64.exe.zst`) и добавляет его размер и SHA-256 в
  манифест. Закрытый seed
  получает из `UPDATE_SIGNING_KEY`; ключ не хранится в Git. Открытый ключ
  `cmd/desktop/update-public-key.txt` встроен через `update_key.go`.
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
  `cmd/desktop/frontend/package.json`.
- `configs/server.example.json` — пример первичной конфигурации сервера. Без
  `-config` сервер создаёт встроенный канал `default`; JSON-конфигурация
  задаёт полное дерево каналов при первом импорте в SQLite. Затем сервер
  загружает имя, каналы и пороги из БД.

Отдельного исполняемого консольного голосового клиента в текущем дереве нет.
Низкоуровневые клиентские команды в `internal/client/command.go` остаются
частью пакета, но точка входа `cmd/client2` удалена.

## Голос и состояние

```text
React UI
  -> Wails service -> clientapp -> client -> transport/udp -> protocol
  -> voice Hub (сервер) -> UDP-получатели того же канала
```

1. `internal/ui/wails/service.go` принимает команды UI и переводит их в
   операции `clientapp`. DTO и проверки входных данных расположены в
   `internal/ui/wails/dto.go`; события для WebView — в `event_bridge.go`.
2. `internal/clientapp/app.go`, `session.go`, `supervisor.go` и
   `reconnect.go` управляют соединением, параллельными циклами, остановкой
   и повторным подключением без зависимости от Wails.
3. `internal/client/` выполняет защищённый handshake, контрольные запросы, heartbeat,
   прием пакетов, обновление состояния и сбор статистики. `internal/protocol/`
   описывает формат пакетов и полезных нагрузок;
   `internal/transport/udp/` читает/пишет датаграммы.
4. `internal/voice/hub.go` — источник истины о сессиях, каналах,
   участниках, потоках экрана и ревизии состояния. `control.go`,
   `delivery.go`, `events.go`, `snapshot.go` и `screen.go` обслуживают
   соответствующие операции. `internal/server/config.go` загружает
   стартовое дерево, `persistent.go` сохраняет/восстанавливает его из БД,
   `pipeline.go` — фоновые процессы сервера. `internal/persist/` хранит
   учётные записи, баны, права, аудит и каналы.
5. Клиент получает snapshot и затем live-события; при расхождении ревизий
   запрашивает снимок заново. Для control-пакетов используется `RequestID`,
   на сервере повторы обрабатывает `voice.RequestCache`.

Аудиотракт: `malgo` захватывает PCM → опционально применяются RNNoise и
VAD/voice gate → Opus-кодер формирует голосовой UDP-пакет → сервер пересылает
его участникам канала, не декодируя → клиентский jitter buffer, Opus-декодер
и микшер передают звук в `malgo`-плеер. Основная сборка трактов находится в
`internal/clientapp/session.go`; устройства и кодеки — в `internal/audio/`,
приём, jitter, декодирование и микшер — в `internal/client/`.
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
  1000 байт UTF-8. Snapshot v6 и state events v2 передают постоянный `user_id`.
- `internal/persist/chat.go` выполняет миграцию SQLite до версии 4, сохраняет
  сообщения и позиции прочтения, выбирает историю по ID. Уникальная пара
  `(sender_id, client_id)` устраняет дубли после reconnect и перезапуска сервера.
- `internal/voice/chat.go` обслуживает отдельную очередь на 128 запросов с
  пределом 4 ожидающих запросов на пользователя. SQLite работает вне голосового
  цикла; доступ проверяется перед запросом и перед возвратом результата.
- `internal/client/chat.go`, `internal/clientapp/chat.go` и
  `internal/ui/wails/service_chat.go` связывают защищённый UDP с Wails DTO.
- `cmd/desktop/frontend/src/features/chat/` содержит интерфейс и ограниченный
  кеш истории, черновиков и отправок. Подтверждения объединяются по ID сообщения;
  `ChatChanged` ускоряет сверку, опрос раз в 5 секунд восстанавливает потери.
  Контекст включает endpoint, публичные ключи сервера/клиента и `user_id`.

## Демонстрация экрана

```text
React screenMedia / ScreenViews
  -> Wails service -> clientapp/media.go -> HTTPS signaling
  -> media.HTTPHandler -> media.Manager / Pion WebRTC SFU
  -> подписчики выбранного потока
```

- `cmd/desktop/frontend/src/features/screen/` содержит захват экрана,
  публикацию, просмотр, профили и статистику. Окно просмотра открывается
  отдельно; список доступных потоков приходит в состоянии сервера.
- `internal/mediasignal/types.go` задаёт JSON-контракт. Серверный
  `internal/media/http.go` обслуживает `/media/publish`,
  `/media/subscribe`, `/media/stop` и `/media/unsubscribe`. Запросы
  авторизуются credential, выданным активной голосовой сессии.
- `internal/media/manager.go` управляет publisher/subscriber и сверяет
  доступ с состоянием `voice.Hub`; остальные файлы `internal/media/`
  реализуют WebRTC, RTP relay/recovery, метрики и TLS identity. Сам экран
  передаётся через WebRTC, а не через голосовой UDP-протокол.
- `internal/clientapp/media.go` подключается к HTTPS-сигнализации с
  проверкой сохранённого отпечатка сертификата. Первое доверие к
  самоподписанному ключу подтверждает пользователь (TOFU); настройка
  хранится в `internal/clientsettings/`.

По умолчанию сервер слушает голосовой UDP-порт `9000`, HTTPS signaling на
`9002` и использует UDP `20000–20100` для WebRTC. Флаги
`-port`, `-media-port`, `-media-min-port`, `-media-max-port`,
`-media-advertised-ip` и `-media-identity` меняют эту конфигурацию;
`-media-port 0` отключает демонстрацию экрана. Для публичного адреса
WebRTC требуется корректный `-media-advertised-ip`.

## Desktop UI и локальные данные

- `cmd/desktop/frontend/src/App.tsx` собирает экран приложения и
  синхронизирует UI с backend. `api.ts` — вызовы Wails и клиентские DTO,
  `model.ts` — преобразования состояния.
- `features/connection/` — подключение, статус и статистика соединения;
  `features/participants/` — строка участника;
  `features/screen/` — демонстрация и просмотр экрана. Стили — в
  `src/*.css`.
- `features/updates/` — кнопка доступного обновления в шапке и модальное окно
  загрузки/установки. Раздела обновлений на странице настроек нет.
- Сгенерированные Wails bindings находятся в
  `cmd/desktop/frontend/bindings/uniclog.io/govts/`. Их следует
  пересоздавать после изменения публичных методов или DTO Wails-сервиса,
  а не редактировать вручную.
- `internal/clientsettings/store.go` сохраняет версионированные настройки
  в каталоге конфигурации пользователя `Govts/settings.json`: имя,
  аудиоустройства, параметры шумоподавления/VAD, тему и доверенные
  media-ключи. Адрес сервера UI хранит в browser localStorage. Отдельный
  `Govts/client.seed` хранит приватный seed пользователя, а
  `Govts/voice-pins.json` — доверенные голосовые серверы (TOFU).
- `internal/clientupdate/service.go` связывает UI с Wails updater, планирует
  проверки и сохраняет автоскачивание в `Govts/updates.json`. `provider.go`
  выбирает стабильный Windows amd64 asset и обязательно проверяет подписанный
  манифест; если в манифесте и релизе есть сжатый `GTS64.exe.zst`, качает его
  и распаковывает на лету, иначе — обычный `GTS64.exe`.
  `internal/updatemanifest/` задаёт формат и Ed25519-проверку.
- `internal/clientupdate/recovery.go` сохраняет прежний EXE и следит за
  подтверждением запуска нового интерфейса с пределом 90 секунд. Windows API
  запуска и проверки процессов — в `process_windows.go`. Helper и watcher
  обрабатываются до логирования и SingleInstance; PID нового обычного клиента
  записывается после SingleInstance. Профиль при откате EXE не откатывается.
- `Govts/pending-update.json` и `Govts/update-recovery/` — план и копии
  восстановления; `Govts/update-recovery-result.txt` — результат отката.
  Черновики чата перед обновлением сохраняются в frontend `localStorage`.

## Проверки, версии и релизы

- Go-тесты находятся рядом с пакетами (`*_test.go`); frontend-тесты — в
  `cmd/desktop/frontend/src/`. Основные локальные проверки:
  `go test ./...`, `go vet ./...`, `npm test --prefix cmd/desktop/frontend`
  и `npm run build --prefix cmd/desktop/frontend`.
- `.github/workflows/test.yml` запускает проверки Go в CI.
  `.github/workflows/release.yml` срабатывает на тег `vX.Y.Z`, сверяет
  его с `clientVersion` в `version/release.json`, собирает Windows desktop и Linux
  server, подписывает desktop через secret `UPDATE_SIGNING_KEY`, загружает
  бинарники, `GTS64.exe.zst`, `signature` и `SHA256SUMS` в draft и затем публикует релиз.
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
