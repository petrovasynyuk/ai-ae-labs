# Лабораторна 4 — той самий граф як ADK HTTP-сервіс

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.5.0. Жодного LLM, ключа або `.env` для базового шляху.
Виконуйте команди **з кореня `ai-ae-labs`**. Потрібні Go, `curl`, а для контейнера — Docker/OrbStack.

## Запуск і чотири перевірки

```bash
go run ./week2/Day4_Agent_as_Service_Deploy/labs4
```

В іншому терміналі:

```bash
BASE=http://127.0.0.1:8080
curl -fsS "$BASE/health"
curl -fsS "$BASE/readyz"
curl -fsS -X POST \
  "$BASE/api/apps/first_graph_agent/users/learner/sessions/demo" \
  -H 'Content-Type: application/json' -d '{}'
curl -fsS -N "$BASE/api/run_sse" \
  -H 'Content-Type: application/json' \
  -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"Мерчант A-114 просить повернення по транзакції txn-2026-07-118845"}]}}'
```

Контрольні точки: `OK` на обох probe-маршрутах; JSON із `"id":"demo"`; потім SSE `data:` з виходами чотирьох вузлів (`classify` → `prepare` → `open_refund_case` → `format`), `refund:last_case_id` і статусом `pending`.
Повторіть лише SSE-запит: отримаєте `already_open`. `/api/run` також доступний через ADK як нестримінговий варіант.

Для додаткових маршрутів Day 3 (статус наявного кейса та відмова):

```bash
# Читання статусу з реєстру:
curl -fsS -N "$BASE/api/run_sse" \
  -H 'Content-Type: application/json' \
  -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"перевір статус rc-txn-2026-07-118845-A-114"}]}}'

# Запит поза доменом (відмова):
curl -fsS -N "$BASE/api/run_sse" \
  -H 'Content-Type: application/json' \
  -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"привіт, яка погода?"}]}}'
```

`appName` — **`first_graph_agent`**, незалежно від назви контейнера чи Cloud Run service.
Це реальний `adkrest.NewServer`, не саморобний `/chat`. Поля запиту **camelCase**.
Workflow-події містять `output`; `content` може бути `null`. LLM-фрагменти тексту тут не очікуються.

## Код і перевірки

`main.go` керує сигналами й адресою; `service.go` монтує ADK під `/api/` і керує drain.
Кроки графа та інструмент імпортуються з [`week2/internal/refund`](../../internal/refund/refund.go), як у Lab 3; топологію композирує [`agent_graph.go`](agent_graph.go) цієї лаби.
Готовий базовий шлях можна змінювати у власному форку; у [завданні](Homework.md) потрібно пояснити та продемонструвати результат, а не лише скопіювати файли.

```bash
go build ./week2/...
go vet ./week2/...
go test -race ./week2/...
go test -v -run '^TestShutdown$' ./week2/Day4_Agent_as_Service_Deploy/labs4
```

Остання команда утримує активний запит і перевіряє обидва сценарії: завершення в межах drain та примусове закриття після timeout.
При SIGTERM сервіс перестає приймати нову роботу й очікує активні запити. `Shutdown` закриває listener: новий `curl` може бачити **connection refused**, а не встигнути отримати 503. На ще відкритому з'єднанні gate повертає 503. Успішний drain — exit 0, перевищення timeout — помилка й ненульовий exit.

Без `PORT` сервіс слухає `127.0.0.1:8080`; з `PORT=9090` — усі інтерфейси на 9090.
`-addr` явно перевизначає адресу. Локальна межа доступу — loopback, у контейнері — опублікований порт.

## Docker: статичний ADK-сервіс, non-root, `FROM scratch`

**Build context — корінь репозиторію**, не `labs4/`: звідти потрібні `go.mod`, `go.sum` та спільний пакет.
`Dockerfile.dockerignore` допускає лише модульні файли й потрібні Go-файли Week 2; `.env` не потрапляє в контекст.

Найкоротший шлях — шість задач із кореневого `Taskfile.yml`. Вони роблять те
саме, що команди нижче, але з правильними теками, тегами й перевірками:

```bash
task week2:day4:docker:build     # docker build --platform linux/amd64 -f ... -t ai-ae-lab4:week2 .
task week2:day4:docker:run       # docker run --detach --read-only --cap-drop ALL -p 127.0.0.1:8080:8080
task week2:day4:docker:smoke     # чотири curl-перевірки одною задачею
task week2:day4:docker:stop      # SIGTERM + доказ drain: exit=0, «drained and stopped»
task week2:day4:docker:inspect   # platform, size, user
task week2:day4:docker:rm        # прибрати контейнер (образ лишається)
```

| Змінна | Типове | Навіщо |
|---|---|---|
| `PORT` | `8080` | публікований порт на loopback; `PORT=18080`, якщо 8080 зайнятий |
| `DOCKER_PLATFORM` | `linux/amd64` | одна платформа для `docker:build` і `docker:run`; `DOCKER_PLATFORM=linux/arm64` — швидша нативна збірка на Apple Silicon (не для Cloud Run) |
| `DOCKER_PLATFORMS` | `linux/amd64,linux/arm64` | список для `docker:multiarch` |
| `REGISTRY` | — | обов'язковий для `docker:multiarch`: префікс реєстру для `--push` |

`task week2:day4:docker:run` спершу прибирає одноіменний контейнер, тож задачу
можна запускати повторно: без цього `docker run` падає з «name is already in
use» після першого перерваного запуску.

`task week2:day4:docker:smoke` створює сесію `demo`. Сесії in-memory і живуть
у процесі сервісу, тож **повторний виклик smoke на тому самому контейнері**
віддає `500` з `session demo already exists` — це стан сервісу, не помилка
образу. Щоб повторити з чистого стану:

```bash
docker rm -f ai-ae-lab4 && task week2:day4:docker:run
```

### Образ на дві архітектури (`docker:multiarch`)

Додаткова задача для публікації: збирає `linux/amd64` і `linux/arm64` одним
manifest-індексом і одразу штовхає його в реєстр. `Dockerfile` уже готовий до
цього крос-компіляцією (`FROM --platform=$BUILDPLATFORM` + `TARGETOS/TARGETARCH`),
тож друга архітектура не коштує нічого зайвого.

```bash
task week2:day4:docker:multiarch REGISTRY=ghcr.io/<ваш-акаунт>
```

```bash
# Той самий виклик зі змінною оточення — форма, зручна для CI
REGISTRY=europe-west1-docker.pkg.dev/<project>/adk-lab4-images \
  task week2:day4:docker:multiarch
```

Тег виходить `<REGISTRY>/ai-ae-lab4:week2-linux-amd64-linux-arm64`, тож
multiarch-образ не перезаписує локальний `ai-ae-lab4:week2`, з яким працюють
`docker:run` і `docker:smoke`.

Три речі, які варто знати перед запуском:

- **`--push` обов'язковий, а не опція.** buildx не вміє `--load` від індексу з
  кількома платформами, і локальний smoke усе одно не запуститься під чужу
  архітектуру. Тому `docker:multiarch` — про публікацію; локально
  перевіряйте `docker:build` + `docker:run`.
- **`--provenance=false`** тримає маніфест пласким. Cloud Run читає простий
  індекс: без attestation у ньому менше шарів і `gcloud run deploy` не має
  зайвих сутностей.
- **Для Cloud Run ця задача не потрібна.** Він вимагає лише `linux/amd64`, тож
  швидший шлях — `docker buildx build --platform linux/amd64 --push` з
  [CLOUD_RUN.md](CLOUD_RUN.md). Multiarch має сенс, коли той самий образ
  беруть і amd64-сервер, і Apple Silicon/ARM-хост — тоді обидва тягнуть свою
  архітектуру з одного тега, без емуляції.

Явні команди, якщо `task` не встановлено:

```bash
docker build --platform linux/amd64 \
  -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile \
  -t ai-ae-lab4:week2 .
docker run --name ai-ae-lab4 --platform linux/amd64 --detach \
  --read-only --cap-drop ALL \
  -p 127.0.0.1:8080:8080 ai-ae-lab4:week2
```

Повторіть чотири `curl` вище. Контейнер запускає Go-бінарник як UID/GID `65532:65532`; shell і package manager у runtime немає. CA bundle включено для майбутніх HTTPS-інструментів, хоча поточний граф не робить зовнішніх викликів.
`linux/amd64` потрібна для Cloud Run; на Apple Silicon збірка cross-compile, а локальний запуск образу — через емуляцію.

```bash
docker image inspect ai-ae-lab4:week2 \
  --format 'platform={{.Os}}/{{.Architecture}} size={{.Size}} bytes user={{.Config.User}}'
docker stop --timeout 35 ai-ae-lab4
docker inspect ai-ae-lab4 --format 'exit={{.State.ExitCode}}'
docker logs ai-ae-lab4
docker rm ai-ae-lab4
```

Очікуємо `exit=0` і `ADK service drained and stopped`. Для короткого графа SIGTERM зазвичай приходить уже після відповіді; доказ активного drain дає `TestShutdown`, не цей idle-smoke.

Перевірено 14.10.2026 (Day 3 граф із класифікатором і валідацією схем): `linux/amd64`,
Docker image inspect `.Size` = **23 970 543 bytes**, UID/GID `65532:65532`.
Усі чотири перевірки пройшли в контейнері (`task week2:day4:docker:smoke`):
`OK`, `OK`, JSON з `"id":"demo"`, чотири SSE-події з `"status":"pending"` (`classify` → `prepare` → `open_refund_case` → `format`);
повторний SSE на тому ж контейнері — `"status":"already_open"`. `task week2:day4:docker:stop`
дав `exit=0` і `ADK service drained and stopped`.

## Advanced: Стрес-тест паралельних SSE-запитів (k6)

Сценарій навантаження збережено у [`loadtest/sse.js`](loadtest/sse.js). Кожна ітерація кожного віртуального користувача (VU) створює окрему унікальну сесію через `POST /api/apps/first_graph_agent/users/learner/sessions/{id}`, після чого надсилає `POST /api/run_sse` у детермінований граф без залучення реальної LLM (`stub/graph-only`).

Вимірюється:
- **TTFB до першої SSE-події** (`res.timings.waiting` — час до приходу заголовків SSE та першого chunk'а події `data:`);
- **Повна тривалість SSE-потоку** (`res.timings.duration` — час до завершення всіх чотирьох подій графа та закриття стріму);
- **Кількість успішних SSE-запитів** (HTTP 200, `text/event-stream`, події `data:`, статус `pending`/`already_open`);
- **Помилки та відсоток відмов**.

Команда запуску (за попередньо піднятого локального сервісу на `:8080`):

```bash
k6 run -e VUS=10 -e DURATION=10s week2/Day4_Agent_as_Service_Deploy/labs4/loadtest/sse.js
```

### Таблиця вимірювань (14.10.2026)

Платформа: **linux/amd64** (Ubuntu, Go 1.27.1, k6 v1.8.2).
Режим: **локальний детермінований граф (stub / without LLM)**, `http://127.0.0.1:8080`.

| Concurrent Users (VUs) | Duration | Successful Requests (SSE) | Throughput (req/s) | TTFB p50 (med) | TTFB p95 | Full Duration p50 (med) | Full Duration p95 | Errors | Error Rate |
|---|---|---|---|---|---|---|---|---|---|
| 1 VU | 10s | 4 844 | 484.35/s | 213 µs | 1.06 ms | 1.09 ms | 1.82 ms | 0 | 0.00% |
| 10 VUs | 10s | 13 417 | 1 340.91/s | 1.03 ms | 3.78 ms | 3.30 ms | 16.42 ms | 0 | 0.00% |
| 25 VUs | 10s | 11 246 | 1 121.58/s | 1.19 ms | 5.08 ms | 14.94 ms | 46.06 ms | 0 | 0.00% |
| 50 VUs | 10s | 10 173 | 1 010.29/s | 1.06 ms | 5.39 ms | 43.27 ms | 83.18 ms | 0 | 0.00% |

## Advanced: Локальне CPU та Heap профілювання (pprof)

Для аналізу продуктивності під навантаженням сервіс підтримує opt-in прапорець `-pprof-addr`.

**Безпека та ізоляція:**
- За замовчуванням pprof **вимкнено** (`-pprof-addr=""`);
- Приймається виключно loopback: `127.0.0.1:<port>` (будь-яка інша адреса, наприклад `0.0.0.0`, відхиляється валідацією при старті);
- Ендпоінти `/debug/pprof/` монтуються на окремий ізольований listener і **ніколи не монтуються на основний сервісний порт** (запит `GET http://127.0.0.1:8080/debug/pprof/` повертає `404 Not Found`).

### Команди зняття профілів

```bash
# 1. Запуск сервісу з окремим локальним pprof listener
go run ./week2/Day4_Agent_as_Service_Deploy/labs4 -pprof-addr=127.0.0.1:6060

# 2. В окремому терміналі — запуск stress-тесту k6
k6 run -e VUS=25 -e DURATION=35s week2/Day4_Agent_as_Service_Deploy/labs4/loadtest/sse.js

# 3. Зняття 30-секундного CPU-профілю під час навантаження
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30

# 4. Зняття heap-профілю пам'яті
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

### Висновки за профілями (знайдені вузькі місця)

1. **Пам'ять (Heap Profile):**
   - Найбільший споживач heap під навантаженням — `session.(*inMemoryService).AppendEvent` (**74.53 MB** flat, **114.04 MB (51.51%)** cum), а також клонування стану сесії `maps.clone` / `maps.Copy` (**38 MB** cum).
   - **Вузьке місце:** Сервіс використовує `session.InMemoryService()`, де кожна сесія зберігає повний журнал подій без обмеження TTL та без фонового витіснення (eviction). За 45 000+ паралельних запитів це призводить до монотонного зростання пам'яті. Для production-розгортання потрібен перехід на зовнішнє (або durable) сховище сесій із налаштованим TTL та автоматичним очищенням неактивних сесій.

2. **Процесор (CPU Profile):**
   - Системні виклики `internal/runtime/syscall/linux.Syscall6` (запис чанків SSE через сокети) займають **13.10%** процесорного часу.
   - Понад **40%** часу CPU припадає на підсистему Go GC та алокатора (`runtime.scanSpan`, `runtime.tryDeferToSpanScan`, `runtime.mallocgc`), що зумовлено високою частотою виділення короткоживучих об'єктів для кожної нової сесії та SSE-події.

## Advanced: Порівняння runtime (scratch, distroless, alpine)

У [`Dockerfile`](Dockerfile) реалізовано спільний етап збірки `build` (`golang:1.27.1`, `CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"`), з якого той самий бінарник `/out/adk-service` копіюється у три різні цільові runtime (multi-stage targets):
- `runtime-scratch` (`FROM scratch`)
- `runtime-distroless` (`FROM gcr.io/distroless/static-debian12:nonroot`)
- `runtime-alpine` (`FROM alpine:3.21`)

Усі три образи запускаються від імені non-root UID/GID `65532:65532`.

### Команди збірки та вимірювання

```bash
# 1. Збірка трьох цільових образів із спільного build stage
docker build --platform linux/amd64 --target runtime-scratch -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile -t ai-ae-lab4:scratch .
docker build --platform linux/amd64 --target runtime-distroless -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile -t ai-ae-lab4:distroless .
docker build --platform linux/amd64 --target runtime-alpine -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile -t ai-ae-lab4:alpine .

# 2. Вимірювання розміру образів
docker image inspect ai-ae-lab4:scratch --format '{{.Size}} bytes'
docker image inspect ai-ae-lab4:distroless --format '{{.Size}} bytes'
docker image inspect ai-ae-lab4:alpine --format '{{.Size}} bytes'

# 3. Вимірювання cold-start (час від запуску контейнера до першого 200 OK на /readyz)
# docker run -d --rm -p 127.0.0.1:8085:8080 --read-only --cap-drop ALL <image>
# і опитування curl до першого коду 200
```

### Таблиця порівняння (14.10.2026)

Платформа: **linux/amd64** (Ubuntu, Go 1.27.1, Docker Engine).
Методика cold start: запуск контейнера (`docker run --read-only --cap-drop ALL`) і вимірювання часу до першого успішного `HTTP 200 OK` на `/readyz` з кроком опитування 5 мс (медіана з 3 прогонів).

| Runtime | Base Image / Digest | Platform | Image Size | Cold Start (до `/readyz`) | CA Bundle Requirement | User (UID/GID) | Shell / Package Manager |
|---|---|---|---|---|---|---|---|
| **scratch** | `scratch` | `linux/amd64` | **24 550 533 bytes** (~24.5 MB) | ~180 ms | **Обов'язковий** (`COPY --from=build`) | `65532:65532` | Відсутній (zero attack surface) |
| **distroless** | `gcr.io/distroless/static-debian12:nonroot` (`sha256:afa5c8...`) | `linux/amd64` | **30 354 795 bytes** (~30.3 MB) | ~198 ms | **Вбудований** (входить до базового образу) | `65532:65532` | Відсутній (захищений runtime) |
| **alpine** | `alpine:3.21` (`sha256:ce6475...`) | `linux/amd64` | **36 271 921 bytes** (~36.2 MB) | ~193 ms | **Вбудований** (пакет `ca-certificates`) | `65532:65532` | `sh`, `apk` (присутні в образі) |

**Висновки:**
1. **Cold start:** Усі три варіанти мають практично однаковий час холодного старту (180–200 мс), оскільки статичний Go-бінарник виконується безпосередньо ядрами Linux через `execve` без проміжних shell-ініціалізаторів.
2. **Розмір:** `FROM scratch` дає мінімально можливий розмір (бінарник + 180 КБ CA bundle). Distroless додає ~5.8 МБ системних метаданих (tzdata, /etc/passwd тощо). Alpine додає ~11.7 МБ через BusyBox, libc та менеджер пакетів apk.
3. **Безпека:** І `scratch`, і `distroless` не містять shell (`/bin/sh`) та package manager, що усуває можливість RCE-виконання довільних скриптів у контейнері.

## Опційно: GCP Cloud Run

[Покроковий Go-only шлях](CLOUD_RUN.md): той самий Dockerfile → Artifact Registry → приватний щодо IAM-викликів Cloud Run → session/SSE smoke → прибирання ресурсів.
Не потрібен для локального завдання й не змінює його бали. GCP потребує billing; безкоштовність не гарантується.

## Межі й типові помилки

- **404 на SSE:** спершу створіть сесію; `appName`, `userId`, `sessionId` мають збігатися. Перезапуск втрачає сесії.
- **`event: error` після HTTP 200:** SSE вже почався; помилка вузла передається окремою подією. Самого HTTP-коду недостатньо.
- **503:** сервіс завершується. Відсутність API-ключа не впливає на readiness цього графа.
- **Порт зайнятий:** зупиніть свій попередній процес або використайте `-addr=127.0.0.1:8081`.
- **Docker не бачить `go.mod`:** перевірте build context `.` у корені `ai-ae-labs`.

Це навчальний однокористувацький сервіс без app-level авторизації, durable storage та платіжного API. Реєстр і сесії в пам'яті; не відкривайте його в загальнодоступну мережу. Cloud Run IAM обмежує викликачів сервісу, але не пов'язує довільний `userId` із людиною автоматично.
