# Сборка, деплой и ручной запуск Sonoryx

## С Windows через сценарии

Запускайте команды из корня репозитория. Для сборки нужны Go, а для веб-релиза ещё Node.js, Zig и `tar`. Деплой использует SSH.

| Компонент | Сборка | Деплой |
| --- | --- | --- |
| Сервер Sonoryx | `npm run build:server` | `npm run deploy:server` |
| Веб-шлюз | `npm run build:gateway` | `npm run deploy:gateway` |
| Веб-клиент | `npm run build:web` | `npm run deploy:web` |
| Веб-шлюз и веб-клиент | `npm run build:webgateway` | `npm run deploy:webgateway` |
| Сервер, веб-шлюз и веб-клиент | `npm run build-all` | `npm run deploy-all` |

Перед первым веб-деплоем [настройте HTTPS и установите systemd-сервис](../apps/webgateway/README.md#сборка-и-запуск). Первый веб-релиз разворачивайте целиком. Деплой по отдельности использует второй компонент из текущего веб-релиза и перезапускает шлюз. `deploy-all` сначала обновляет Sonoryx, затем веб-релиз.

Команды `powershell.exe -File .\scripts\build-server.ps1` и `powershell.exe -File .\scripts\deploy-server.ps1` тоже доступны. Десктоп собирается через `npm run build:desktop` и получает подписанные обновления из GitHub Releases. Версии приложений заданы в [version.json](../version/version.json).

## Ручной запуск на Linux после копирования файлов

На Windows соберите артефакты и скопируйте их на хост. Замените `SERVER_HOST` своим доменом или IP.

```powershell
npm run build-all
$target = 'admin@SERVER_HOST'
scp bin/sonoryx-server bin/sonoryx-webgateway-linux-amd64.tar.gz configs/webgateway.example.json "${target}:~/"
ssh $target
```

Команды ниже предполагают Linux amd64 и существующий каталог `/opt/sonoryx` с правом записи. На Linux достаточно готовых файлов. Для запуска в переднем плане оставьте SSH-сессию открытой. Чтобы отключиться от SSH и оставить процесс работать, запустите его в `tmux`. Перед ручной заменой работающего компонента остановите его штатно и дождитесь завершения.

**Сервер Sonoryx.** Если он уже работает и нужен только веб, переходите сразу к шлюзу. При ручном обновлении используйте действующие `server.json`, базу, ключи и параметры запуска. Пример с портами из `deploy-server.ps1`:

```sh
cd /opt/sonoryx
install -m 0755 ~/sonoryx-server ./sonoryx-server.manual
./sonoryx-server.manual -config server.json -db sonoryx.db \
  -port 9000 -file-port 9001 -media-port 9002 \
  -media-min-port 20000 -media-max-port 20100 -media-advertised-ip PUBLIC_IP \
  -voice-redundancy -voice-uplink-redundancy -public-status -media-adaptive-bitrate
```

Замените `PUBLIC_IP` публичным IP сервера. Для новой установки возьмите за основу [пример server.json](../configs/server.example.json).

**Веб-шлюз и статика.** На том же хосте распакуйте архив и запустите шлюз:

```sh
cd /opt/sonoryx
mkdir -p webgateway/manual
cd webgateway
tar -xzf ~/sonoryx-webgateway-linux-amd64.tar.gz -C manual
chmod 0755 manual/sonoryx-webgateway
cp -n ~/webgateway.example.json webgateway.manual.json
```

В `webgateway.manual.json` задайте `webRoot` как `manual/web`, `stateDir` как путь к постоянному каталогу данных и `server` как `127.0.0.1:9000`. Укажите действующий `serverKey`, файлы TLS-сертификата и ключа, а для прямого доступа задайте `listen` как `0.0.0.0:8443` и `origin` как `https://ВАШ_ДОМЕН:8443`. Настройте `mediaFingerprint`, если нужна демонстрация экрана. Если Sonoryx защищён паролем, задайте `serverPasswordEnv` и передайте пароль через указанную переменную окружения. При переходе с автоматического деплоя сохраните существующий `stateDir`, чтобы сохранить профили и настройки. [Поля конфигурации](../apps/webgateway/README.md#сборка-и-запуск)

```sh
./manual/sonoryx-webgateway -config webgateway.manual.json
```

Откройте в браузере адрес из `origin`. Для прямого подключения откройте на хосте TCP-порт HTTPS и UDP-диапазон WebRTC шлюза (`21000–21100` в примере конфигурации). Шлюз сам отдаёт веб-клиент из `manual/web`.
