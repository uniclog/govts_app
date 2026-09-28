# Govts

Govts — настольный клиент голосовой связи и сервер на Go. Проект поддерживает
каналы, обработку звука и демонстрацию экрана. Go-модуль — `uniclog.io/govts`.

Подробная структура кода описана в [`PROJECT_MAP.md`](PROJECT_MAP.md). Рабочие заметки в
`readme_docs/` хранятся локально и не входят в Git-репозиторий.

## Текущая архитектура

Клиент и сервер используют UDP transport (по умолчанию порт `9000`) для
control-пакетов и Opus voice-пакетов. Демонстрация экрана использует встроенный
в тот же сервер HTTPS signaling и WebRTC/DTLS-SRTP с Pion SFU.

```text
microphone
    -> PCM frames
    -> RNNoise filter (optional)
    -> WebRTC VAD + voice gate (optional)
    -> Opus encoder
    -> VoicePacket
    -> ClientPacketConn -> DatagramCodec -> UDP
    -> UDP -> ServerPacketConn -> DatagramCodec
    -> участники того же канала
    -> Opus decoder
    -> audio player
```

Сервер не декодирует звук: `VoicePacket.Payload` остаётся непрозрачным набором
байтов и пересылается другим клиентам.

### Реализовано

- аутентификация клиента по Ed25519-ключу с защищённым рукопожатием;
  после него голосовые и управляющие UDP-пакеты шифруются AES-GCM;
- server-side sessions с криптографически случайным `SessionID`;
- самостоятельная доменная модель каналов с постоянным `ChannelID`,
  иерархией, метаданными и Opus-профилем;
- потокобезопасный server-side registry каналов, участников и
  `StateRevision`;
- встроенный канал `default` без конфигурационного файла и однократный импорт
  полного дерева каналов из JSON в SQLite;
- локальная консоль сервера для просмотра состояния и управления учётными
  записями, правами, банами и порогами входа в каналы;
- подтверждаемый heartbeat каждые 5 секунд с отдельным ACK deadline 3 секунды;
- автоматический reconnect с задержками `1s → 2s → 4s → 4s...`, новой
  сессией и входом в помеченный `default`-канал;
- удаление session после 30 секунд неактивности;
- подключение и переключение канала через `JoinChannel`;
- разделение входящих voice и control packets;
- Opus encode/decode;
- захват микрофона через malgo; при отсутствии или отключении устройства клиент
  остаётся в голосовом канале в состоянии mute и продолжает принимать звук;
- воспроизведение через malgo с переключением устройства вывода;
- `RequestID`, ожидание ответа и повтор control-запроса;
- server-side deduplication через `RequestCache`;
- явный disconnect клиента;
- единая codec-граница для всех логических пакетов с явным направлением,
  endpoint и отдельным контекстом получателя (`KeyOwnerID`);
- разные transport-типы для connected client socket и unconnected server
  socket;
- безопасный drop повреждённых/rejected датаграмм без остановки receive loop;
- paged `ServerSnapshot` и атомарное клиентское состояние;
- live-события подключения, перехода и отключения участников; recovery через
  snapshot и metadata revision check каждые 5 секунд;
- синхронизация mute/deafen через сервер, индикаторы этих состояний у участников
  и индикаторы речи в настольном клиенте;
- независимые RNNoise-шумоподавление и WebRTC VAD/voice gate с режимами
  `level`, `vad`, `hybrid`, pre-roll 60 мс и hangover 300 мс;
- `ClientViewState`, глубокие копии и bounded-подписка на изменения UI;
- Wails 3 desktop-клиент с React/TypeScript UI каналов, журналом событий,
  настройками аудио и сохранением локальных параметров;
- громкость и отключение звука отдельных участников, темы оформления и
  окно статистики соединения;
- несколько одновременных демонстраций экрана в канале с добровольной
  подпиской на выбранного автора, VP8 и статичными карточками без превью;
- автоматически создаваемая TLS identity media-сервера и подтверждение её
  SHA-256 отпечатка клиентом при первом подключении (TOFU);
- постоянные учётные записи по публичному ключу, права `kick`/`ban`/`drag`,
  порог входа в канал и журналирование отказов в доступе.

### Известные ограничения

- jitter buffer использует фиксированное окно; ещё нет Opus PLC и адаптивной
  задержки;
- handshake deduplication требует стабильного `IP:port` на время retry;
- открытая регистрация по ключу требует дополнительной защиты публичного
  сервера, если доступ должен быть только по приглашению;
- потеря `client.seed` означает потерю прежней учётной записи; первый контакт
  с голосовым сервером (TOFU) нужно сверять через доверенный канал;
- TURN/TCP fallback и передача системного звука не реализованы;
- нет удалённого RCON и сетевого API создания каналов; после первичного
  импорта структура каналов не редактируется, кроме порога входа;
- live-события доставляются best effort; потеря последнего события обнаруживается
  периодической проверкой revision. Консольная история также best effort.
- VAD распознаёт любую речь, а не владельца микрофона; `hybrid` отсекает фоновый
  разговор только тогда, когда он тише настроенного порога.

## Структура проекта

```text
cmd/server/              запуск UDP-сервера
cmd/desktop/             Wails desktop entrypoint и React frontend
cmd/versionbump/          обновление версий сборок
internal/audio/          устройства, PCM и Opus
internal/appversion/     разбор и сравнение версий
internal/auth/           криптографическое рукопожатие
internal/client/         состояние и goroutine клиента
internal/clientapp/      общий lifecycle, reconnect и session orchestration
internal/clientsettings/ локальные настройки desktop-клиента
internal/domain/         общие модели каналов, участников и ревизии
internal/identity/       клиентский/серверный ключи и доверие к серверу
internal/media/          HTTPS signaling и WebRTC media server
internal/persist/        SQLite: учётные записи, права и каналы
internal/protocol/       бинарный формат пакета
internal/server/         bootstrap, локальная консоль и фоновые процессы
internal/transport/udp/  чтение и запись UDP
internal/ui/wails/       bindings, безопасные DTO и Wails event bridge
internal/voice/          sessions, channels, routing и request cache
```

Пакеты внутри `internal` доступны только коду этого Go-модуля. Это позволяет
менять внутреннюю архитектуру, не создавая преждевременный публичный API.

## Запуск

Для сборки требуется Go `1.27.1`. Настольному клиенту также нужны Node.js/npm,
доступные аудиоустройства и WebView2 на Windows. Сервер можно запускать без
аудиоустройств.

Запустить сервер:

```bash
go run ./cmd/server
```

По умолчанию голос и управление используют UDP `9000`, HTTPS media signaling —
TCP `9002`, а WebRTC — UDP `20000–20100`. Учётные записи и каналы хранятся
в `govts.db`, серверный ключ подписи — в `govts-voice.seed`. Пути можно задать
флагами `-db` и `-voice-identity`. Для публичного сервера укажите его
достижимый IP и откройте соответствующие порты:

```bash
go run ./cmd/server -media-advertised-ip 203.0.113.10
```

Файлы `govts-media.crt` и `govts-media.key` создаются автоматически. При первом
просмотре или запуске демонстрации клиент показывает отпечаток; его следует
сверить с `fingerprint` в консоли сервера. Публичный CA и домен не требуются.
Screen sharing можно отключить флагом `-media-port 0`; диапазон меняется через
`-media-min-port` и `-media-max-port`.

Другой голосовой порт задаётся через `-port` (1–65535); если `-media-port`
не указан, HTTPS signaling слушает на два порта выше:

```bash
go run ./cmd/server -port 9100
```

В настольном клиенте укажите тот же адрес и порт, например
`192.168.1.50:9100`.

Без конфигурационного файла создаётся единственный канал `default`. При
первом запуске с пустой БД поле JSON `channels` задаёт полное дерево каналов
и не дополняется встроенным каналом:

```bash
go run ./cmd/server -config configs/server.example.json
```

Пометьте ровно один канал полем `"default": true`: при каждом подключении и
переподключении клиент войдёт именно в него. Его `min_join_level` и
`max_users` должны быть равны нулю. Без пометки будет выбран первый
неограниченный канал. После первичного импорта сервер загружает конфигурацию
из БД; изменение JSON уже не меняет работающий сервер. Для резервной копии
остановите сервер или сохраняйте согласованный снимок SQLite вместе с WAL.

Новая учётная запись создаётся при первом успешном подключении с публичным
ключом клиента. Начальные права: `join_level=0`, без `kick`, `ban` и `drag`.
Канал с `min_join_level=25` доступен пользователям с `join_level >= 25`.
Числовой уровень остаётся серверным параметром: desktop UI показывает только
доступность канала, но не уровень пользователя. Отказ во входе отображается в
журнале клиента; сервер пишет `user_id`, `session_id`, `channel_id` и краткую
причину, не выводя ключ и числовой уровень.

Локальные команды сервера:

```text
help
status
channels
channel <id|name>
users
user <session-id>
accounts
account <user-id>
account set-join-level <user-id> <0..65535>
account set-permission <user-id> <kick|ban|drag> <on|off>
account set-owner <user-id>
account ban <user-id>
account unban <user-id>
channel set-min-join-level <channel-id> <0..65535>
```

`accounts` показывает ID и отпечатки публичных ключей для сверки пользователя.
`set-owner` защищает учётную запись от `kick`, `ban` и `drag` со стороны других
пользователей и от бана через консоль. Это **не** выдаёт право модерации и не
повышает уровень входа: их назначают отдельными командами. Владельцев может
быть несколько; команды снятия статуса владельца пока нет. Назначайте его
только после проверки отпечатка ключа. Право `drag` позволяет перемещать свою
текущую сессию и другие сессии той же учётной записи; `kick` и `ban`
собственной учётной записи запрещены.

Настольный клиент использует Wails `v3.0.0-beta.26`, React и TypeScript. Команды
ниже запускают закреплённую версию Wails CLI через Go; отдельно устанавливать
CLI не нужно.

Запустить desktop UI с hot reload:

```powershell
npm run dev:desktop
```

Собрать standalone desktop-клиент:

```powershell
npm run build:desktop
```

Результат на Windows — `bin/Govts.exe`. В интерфейсе доступны подключение,
каналы, статистика соединения, настройки микрофона и воспроизведения, громкость
участников, темы оформления и демонстрация экрана. Пользователь с правом `drag`
может перетащить себя или другого участника ЛКМ на строку другого канала; сервер
проверяет доступ цели и вместимость канала.

Если микрофон отсутствует при подключении или пропадает во время работы, клиент
не выходит из канала: приём звука и остальные функции продолжают работать, а
кнопка микрофона показывает `Нет микрофона`. Когда устройство снова станет
доступно, выберите его в настройках звука и затем явно снимите mute. Значки возле
имени участника показывают выключенный микрофон и deafen всем клиентам канала.

Изменение формата snapshot и передача mute/deafen повышают уровень совместимости
защищённого протокола до `0.2.5`. Сервер отклоняет более старые защищённые
клиенты, поэтому сервер и desktop-клиенты для этого обновления следует обновлять
вместе.

Команды сборки через Wails автоматически увеличивают patch-версию в
`cmd/desktop/version.txt` и синхронизируют её с `build/config.yml`. Версия
отображается в заголовке окна. Отдельный релизный workflow собирает код без
повышения версии.

Desktop-клиент сохраняет display name и аудионастройки в
`%APPDATA%\Govts\settings.json`. Сохраняются выбранные устройства ввода и
вывода, deafen, RNNoise, VAD, тему оформления и доверенные отпечатки
media-серверов. Адрес сервера хранится отдельно во frontend `localStorage`.
Клиентский seed Ed25519 хранится отдельно в `%APPDATA%\Govts\client.seed`
как Base64-строка; его нельзя публиковать или включать в обычные настройки.
Для переноса учётной записи нужен безопасный перенос этого файла. Ключ
голосового сервера запоминается по адресу в `voice-pins.json` (TOFU); при его
неожиданной смене подключение отклоняется.
При первом запуске в поле подключения указан `127.0.0.1:9000`; для другого
сервера введите его IP или адрес с портом, например `192.168.1.50:9000`.

Linux-сервер `amd64` можно собрать на Windows командой
`.\scripts\build-server.ps1`: она повышает `cmd/server/version.txt` и создаёт
`bin/govts-server`. Версию сервера можно проверить флагом `-version`.
`scripts/deploy-server.ps1` загружает бинарник и перезапускает сервер в
`tmux`-сессии `govts-server`. Чтобы открыть серверную консоль после деплоя,
подставьте адрес своего сервера:

```bash
ssh -t user@server 'tmux attach-session -t govts-server'
```

Для выхода без остановки сервера нажмите `Ctrl-b`, затем `d`. Вывод сессии
дополнительно записывается в `/opt/govts/server.log`. При первом переходе с
`nohup` скрипт остановит прежний сервер и запустит его в `tmux`.

## Релизы GitHub

Workflow [`.github/workflows/release.yml`](.github/workflows/release.yml)
запускается при отправке тега `vX.Y.Z`. Он проверяет, что версия тега совпадает
с закоммиченным `cmd/desktop/version.txt`, собирает Windows-клиент и Linux-сервер
и публикует оба файла в GitHub Releases. Перед созданием тега закоммитьте и
отправьте код вместе с нужной версией. Пример для PowerShell:

```powershell
$releaseVersion = (Get-Content cmd/desktop/version.txt -Raw).Trim()
git tag -a "v$releaseVersion" -m "Release v$releaseVersion"
git push origin "v$releaseVersion"
```

## Проверки проекта

```bash
go test ./...
go vet ./...
npm test --prefix cmd/desktop/frontend
npm run build --prefix cmd/desktop/frontend
```

CI также запускает `go test -race ./...` на Linux; для него требуются системные
audio development packages, устанавливаемые workflow.

## Бинарный UDP-протокол

Каждый datagram содержит 17-байтный заголовок и payload:

```text
offset   field       type     size
0        Type        uint8    1 byte
1..8     SessionID   uint64   8 bytes, Big Endian
9..12    Sequence    uint32   4 bytes, Big Endian
13..16   RequestID   uint32   4 bytes, Big Endian
17..N    Payload     []byte   оставшиеся bytes
```

Типы пакетов:

```text
1  Hello
2  Voice
3  HelloAck
4  Heartbeat
5  Disconnect
6  JoinChannel
7  JoinChannelAck
8  Error
9  StateSnapshotRequest
10 StateSnapshotAck
11 HeartbeatAck
12 SessionInvalid
13 StateEvent
14 MediaCredentialRequest
15 MediaCredentialAck
16 ServerVersionTooOld
17 AuthInit
18 AuthChallenge
19 AuthFinish
20 AuthAck
21 Kick
22 Ban
23 Drag
24 ModerationAck
25 AccountPrivileges
```

`Sequence` задаёт порядок voice-пакетов. `RequestID` связывает control request
с response и остаётся одинаковым для всех retry одной логической операции.
`AuthInit`/`AuthChallenge`/`AuthFinish`/`AuthAck` заменяют анонимный
`Hello`/`HelloAck`, который сервер отклоняет. После рукопожатия пакеты сессии
защищены AES-GCM; для каждого datagram проверяются подлинность и защита от
повторной отправки. Transport принимает максимум `MaxWireDatagramSize == 1250`
байт: до 1217 байт логического пакета (17-байтный заголовок и payload до
1200 байт) плюс 33 байта накладных данных защищённой записи. Буфер чтения
имеет дополнительный байт для обнаружения превышения лимита.

Все пакеты проходят через `DatagramCodec`. Его `DatagramContext` отдельно
передаёт направление, endpoint и `KeyOwnerID`. Поэтому при серверной пересылке
`VoicePacket.SessionID` продолжает обозначать говорящего, а `KeyOwnerID` —
конкретного получателя. Некорректный вход классифицируется как
`ErrRejectedDatagram` и тихо отбрасывается; ошибки socket и внутренние ошибки
transport остаются фатальными.

Основные параметры протокола:

| Параметр | Значение |
|---|---:|
| UDP read buffer | 1251 bytes |
| Максимальный datagram | 1250 bytes |
| Максимальный payload | 1200 bytes |
| Handshake timeout | 1 секунда на попытку, до 3 попыток |
| Control request timeout | 3 секунды на попытку |
| Control request attempts | 3 |
| Heartbeat interval | 5 секунд |
| Session timeout | 30 секунд |
| Session cleanup interval | 5 секунд |
