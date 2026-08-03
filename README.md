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
go build -o bin/crawler-api   ./cmd/api
go build -o bin/crawler-crawl ./cmd/crawl
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
| `OLLAMA_URL` | `http://127.0.0.1:11434` | untuk title-suggest |
| `OLLAMA_MODEL` | `qwen2.5:7b-instruct` | model Ollama |

Endpoint:

- `GET  /health` — publik, cek DB + jumlah papers
- `GET  /api/v1/datasets/search?q=...&source=...&year_min=...&year_max=...&has_dataset=...&limit=20&offset=0`
- `GET  /api/v1/datasets/trend?q=...&year_min=2015&year_max=2030`
- `POST /api/v1/citations/suggest` — `{"paragraph": "...", "limit": 5}`
- `POST /api/v1/similarity/check` — `{"text": "...", "limit": 5}`
- `POST /api/v1/research/title-suggest` — `{"topic": "...", "program": "...", "n": 5}`

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

## Roadmap

- [ ] Crawler tambahan: data.go.id (CKAN API), BPS, OpenAlex
- [ ] Scheduler internal (cron/systemd timer): re-crawl mingguan
- [ ] Webapp page: `/research/data-finder` consume endpoint search

## Catatan deploy

`garuda.kemdikbud.go.id` ngga resolve dari DNS internal `studio-server`
(systemd-resolved nolak `.kemdikbud.go.id`). Pakai EPrints atau alternatif
lain dulu sampai DNS dibetulin.
