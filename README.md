# crawler-service — layanan crawling akademik bersama (Go)

Shared service untuk semua project (studio-revisi, wizard-research, dll):
crawl repositori EPrints kampus Indonesia → Postgres → HTTP API dengan
auth API key per client. **Detail lengkap: lihat [PRD.md](PRD.md).**

> Status: produksi (API :8770) + 1 crawler generik EPrints.
> Target awal: `eprints.ums.ac.id`. Pattern berlaku untuk repository EPrints
> Indonesia lainnya (UI, ITB, ITS, UGM, UNY, UPI dll.).

## Struktur

```
crawler-service/
├── cmd/
│   ├── api/            # binary HTTP API (crawler-api)
│   └── crawl/          # binary crawler EPrints (crawler-crawl)
├── internal/
│   ├── api/            # handler + middleware (auth, rate limit, CORS)
│   ├── crawler/        # crawler generik EPrints (robots.txt, delay, retry)
│   ├── db/             # koneksi Postgres + skema + upsert
│   ├── model/          # tipe Paper bersama
│   └── ollama/         # klien Ollama /api/generate
├── deploy/crawler-api.service
├── scripts/seed-crawl.sh
├── go.mod
└── README.md
```

## Build

```bash
go build -o bin/crawler-api    ./cmd/api
go build -o bin/crawler-crawl  ./cmd/crawl
go build -o bin/crawler-worker ./cmd/worker
```

## Jalankan crawler EPrints

```bash
# UMS, keyword "machine learning", 2 halaman search (max 40 record)
bin/crawler-crawl \
  -base-url https://eprints.ums.ac.id \
  -query "machine learning" \
  -max-pages 2 \
  -out output/ums-ml.json
```

Hasil otomatis di-upsert ke Postgres bila `CRAWLER_DATABASE_URL` bisa
diakses (skema dibuat otomatis). Pakai `-no-db` untuk skip Postgres,
`-out` untuk simpan JSON.

Output JSON per item:

```json
{
  "source": "eprints:eprints.ums.ac.id",
  "source_id": "59935",
  "url": "https://eprints.ums.ac.id/id/eprint/59935",
  "title": "...",
  "authors": ["..."],
  "year": 2018,
  "journal": "",
  "keywords": ["..."],
  "abstract": "...",
  "dataset_urls": ["https://eprints.ums.ac.id/.../ARTICLE PUBLICATION.pdf"],
  "has_dataset": true,
  "is_open_access": true,
  "scraped_at": "2026-05-21T06:30:00Z"
}
```

## Jalankan API

```bash
bin/crawler-api --addr 127.0.0.1:8770
```

Env:

| Variabel | Default | Keterangan |
| --- | --- | --- |
| `CRAWLER_DATABASE_URL` | `postgresql://postgres:postgres@127.0.0.1:5432/revisi_crawler` | Postgres DSN |
| `CRAWLER_API_HOST` / `CRAWLER_API_PORT` | `127.0.0.1` / `8770` | bind API (override via `--addr`) |
| `CRAWLER_API_KEYS` | (kosong = auth mati) | `key:client,key2:client2` — request wajib header `X-API-Key` |
| `CRAWLER_RATE_LIMIT_PER_MINUTE` | `120` | sliding window per client |
| `CRAWLER_CRAWL_CONCURRENCY` | `2` | job crawl on-demand paralel maksimal |
| `CRAWLER_REDIS_ADDR` | (kosong = in-memory) | Redis untuk job queue durable (asynq): job tahan restart, retry otomatis |
| `CRAWLER_CDP_URL` | (kosong = render_js mati) | endpoint CDP browser headless, mis. `ws://127.0.0.1:9222` (Lightpanda); boleh banyak dipisah koma → round-robin fleet |
| `CRAWLER_PROXY_URLS` | (kosong = koneksi langsung) | daftar proxy dipisah koma (`http`/`https`/`socks5`), dirotasi round-robin; 403/429 di-retry dengan proxy berikutnya |
| `CRAWLER_WEBHOOK_SECRET` | (kosong = tanpa signature) | secret HMAC-SHA256 untuk header `X-Signature` webhook |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | untuk title-suggest |
| `OLLAMA_MODEL` | `qwen2.5:7b-instruct` | model Ollama |

Endpoint:

- `GET  /health` — publik, cek DB + jumlah papers
- `GET  /api/v1/datasets/search?q=...&source=...&year_min=...&year_max=...&has_dataset=...&limit=20&offset=0`
- `GET  /api/v1/datasets/trend?q=...&year_min=2015&year_max=2030`
- `POST /api/v1/citations/suggest` — `{"paragraph": "...", "limit": 5}`
- `POST /api/v1/similarity/check` — `{"text": "...", "limit": 5}`
- `POST /api/v1/research/title-suggest` — `{"topic": "...", "program": "...", "n": 5}`
- `POST /api/v1/scrape` — on-demand: `{"url": "...", "only_main_content": true, "timeout_ms": 30000, "render_js": false}` → fetch + readability + markdown langsung (tanpa Postgres); `render_js: true` merender via browser CDP untuk situs SPA
- `POST /api/v1/crawl` — async: `{"url": "...", "max_pages": 10, "max_depth": 2, "delay_ms": 1000, "persist": false, "render_js": false}` → `202 {"job_id": "..."}`; BFS link internal satu host, patuh robots.txt; `persist: true` menyimpan tiap halaman ke tabel `scraped_pages`
- `GET  /api/v1/crawl/{id}` — poll status job (`pending|running|completed|failed`) + hasil `pages[]` markdown; job disimpan in-memory 30 menit
- `POST /api/v1/extract` — `{"url": "..." | "markdown": "...", "schema": {...}, "prompt": "..."}` → scrape (bila url) + Ollama structured output sesuai JSON schema
- `POST /api/v1/map` — `{"url": "...", "limit": 100, "search": "..."}` → daftar URL internal situs via sitemap.xml (fallback BFS ringan)
- `GET  /api/v1/pages/search?q=...&host=...&limit=20&offset=0&include_markdown=true` — FTS halaman hasil crawl yang di-persist
- `POST /api/v1/batch/scrape` — async multi-URL: `{"urls": ["..."], "delay_ms": 1000, "persist": false, "render_js": false, "webhook": "https://..."}` (max 50 URL) → `202 {"job_id": "..."}`
- `GET  /api/v1/batch/scrape/{id}` — poll status job batch (format sama dengan crawl)

Field `webhook` (di `/crawl` dan `/batch/scrape`): saat job selesai, service
mem-POST ringkasan `{job_id, kind, status, total, error_count, completed_at}`
ke URL tersebut (retry 3×). Bila env `CRAWLER_WEBHOOK_SECRET` di-set, request
ditandatangani HMAC-SHA256 di header `X-Signature: sha256=<hex>`.

## JS rendering (Lightpanda)

Untuk situs SPA (React/Vue dll), `render_js: true` merender halaman lewat
browser headless eksternal via CDP. Direkomendasikan [Lightpanda]
(https://github.com/lightpanda-io/browser) — ~16× lebih hemat RAM dan ~9×
lebih cepat dari Chrome headless; kompatibel juga dengan Chrome/Chromium.

```bash
# opsi 1: binary (systemd unit tersedia di deploy/lightpanda.service)
curl -L -o ~/.local/bin/lightpanda \
  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-x86_64-linux
chmod +x ~/.local/bin/lightpanda
~/.local/bin/lightpanda serve --obey-robots --host 127.0.0.1 --port 9222

# opsi 2: Docker
docker run -d --name lightpanda -p 127.0.0.1:9222:9222 lightpanda/browser:nightly

# lalu set env untuk crawler-api
export CRAWLER_CDP_URL=ws://127.0.0.1:9222
```

Tanpa `CRAWLER_CDP_URL`, request dengan `render_js: true` ditolak `422`.
Lightpanda masih Beta — bila sebuah situs gagal render, fallback pakai
Chrome headless (`chromium --headless --remote-debugging-port=9222`) tanpa
mengubah kode.

## Skala horizontal (fleet)

Dengan `CRAWLER_REDIS_ADDR`, job crawl/batch masuk queue Redis dan bisa
dikerjakan banyak proses sekaligus — termasuk di mesin berbeda:

```bash
# mesin A: API (punya worker embedded)
CRAWLER_REDIS_ADDR=10.0.0.1:6379 bin/crawler-api

# mesin B, C, ...: worker tambahan (tanpa HTTP)
CRAWLER_REDIS_ADDR=10.0.0.1:6379 \
CRAWLER_CRAWL_CONCURRENCY=8 \
CRAWLER_CDP_URL=ws://127.0.0.1:9222,ws://127.0.0.1:9223 \
  bin/crawler-worker
```

asynq membagi job otomatis antar seluruh worker; `job_id` tetap bisa
di-poll lewat API mana pun karena state disimpan di Redis. Unit systemd:
`deploy/crawler-worker.service`.

Tiap worker boleh menunjuk beberapa endpoint CDP (fleet browser) dan
daftar proxy sendiri, sehingga kapasitas render dan jalur keluar bisa
ditambah tanpa mengubah kode.

## Etika & rate-limit crawler

- Patuh `robots.txt` (per user-agent group)
- Delay antar-request 1.5 detik (ubah via `-delay`)
- Retry ringan (2x) hanya untuk 5xx/429
- User-Agent: `revisi-studio-crawler/0.2 (+https://revisi-studio.id)`

## Seed crawl

```bash
scripts/seed-crawl.sh   # crawl daftar repo terkurasi, upsert idempotent
```

## Deploy (systemd user unit)

```bash
go build -o bin/crawler-api ./cmd/api
cp deploy/crawler-api.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now crawler-api
```

Backup `revisi_crawler` **tidak** diurus repo ini — sudah tercakup timer
`revisi-db-backup.timer` milik repo `studio-revisi-core`
(`deploy/backup/revisi-db-backup.sh`, pg_dump harian + retensi 14 hari).

## Roadmap

- [ ] Crawler tambahan: data.go.id (CKAN API), BPS, OpenAlex
- [ ] Scheduler internal (cron/systemd timer): re-crawl mingguan
- [ ] Webapp page: `/research/data-finder` consume endpoint search

## Catatan deploy

`garuda.kemdikbud.go.id` ngga resolve dari DNS internal `studio-server`
(systemd-resolved nolak `.kemdikbud.go.id`). Pakai EPrints atau alternatif
lain dulu sampai DNS dibetulin.
