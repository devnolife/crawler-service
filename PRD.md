# PRD — Crawler Service

**Layanan crawling akademik bersama (shared service) untuk semua project.**

| | |
|---|---|
| Repo | `github.com/devnolife/crawler-service` |
| Versi API | 0.2.0 |
| Port kanonik | `8770` (host `127.0.0.1`) |
| Runtime | Python 3.10 · FastAPI + Uvicorn · Scrapy · PostgreSQL |
| Status | Produksi (systemd `revisi-crawler.service` — akan di-rename `crawler-service`) |

---

## 1. Latar Belakang & Masalah

Beberapa project (studio-revisi, wizard-research, dan project riset lain) membutuhkan
data yang sama: **skripsi/tesis dan dataset publik dari repositori kampus Indonesia**
(EPrints: UMS, UI, ITB, ITS, UGM, UNY, UPI, dst.), plus utilitas turunannya
(saran sitasi, cek kemiripan, saran judul).

Sebelumnya crawler tertanam di dalam monorepo studio-revisi, sehingga:
- Project lain tidak bisa memakainya tanpa meng-clone seluruh studio-revisi.
- Tidak ada auth — di server shared, siapa pun bisa memakai/membebani service.
- Tidak ada isolasi beban antar pemakai.

## 2. Tujuan

1. Satu layanan crawling + search yang dipakai **lintas project via HTTP API**.
2. **Auth per client** (API key per project) dan **rate limit per client**.
3. Deploy mandiri: repo sendiri, venv sendiri, unit systemd sendiri.

### Non-tujuan
- Bukan mesin crawl real-time on-demand — crawl berjalan batch/terjadwal, API
  membaca hasil yang sudah tersimpan di Postgres.
- Bukan pengganti API literatur eksternal (OpenAlex/CORE/Semantic Scholar) —
  fokusnya repositori kampus Indonesia yang tidak terindeks baik di sana.
- Tidak menyimpan full-text PDF; hanya metadata + abstrak + link.

## 3. Pengguna & Konsumen

| Konsumen | Cara pakai | Endpoint utama |
|---|---|---|
| studio-revisi webapp | proxy `/api/research/*` → crawler | search, similarity, title-suggest |
| studio-revisi backend (AI core) | fitur data-finder riset | search, trend |
| wizard-research | pencarian referensi lokal | search, citations |
| Project masa depan | daftar via API key baru | semua |

## 4. Arsitektur

```
┌─────────────┐   scrapy crawl (cron/manual)   ┌──────────────┐
│ EPrints     │ ─────────────────────────────▶ │ PostgreSQL   │
│ repos (.id) │                                │ tabel papers │
└─────────────┘                                └──────┬───────┘
                                                      │ read
                  X-API-Key per client         ┌──────▼───────┐
  studio-revisi ─────────────────────────────▶ │ FastAPI :8770│
  wizard-research ───────────────────────────▶ │ api/main.py  │──▶ Ollama
  project lain ──────────────────────────────▶ └──────────────┘   (citation/title LLM)
```

### Komponen
- **`revisi_crawler/`** — Scrapy project.
  - `spiders/eprints.py` — spider generik EPrints (Dublin Core meta tags);
    parameter `base_url`, `query`, `max_pages`. Sopan: delay 1.5s, 2 req/domain.
  - `items.py` — schema `ResearchItem`; `pipelines.py` — timestamp; `db.py` —
    upsert ke Postgres.
- **`api/main.py`** — FastAPI, baca Postgres + panggil Ollama untuk fitur LLM.
- **`scripts/seed-crawl.sh`** — seed crawl kampus-kampus umum.
- **`deploy/crawler-api.service`** — unit systemd user.

### Data
Tabel `papers`: `source` (mis. `eprints:eprints.ums.ac.id`), `source_id`,
`title`, `authors[]`, `journal`, `year`, `url`, `abstract`, `keywords[]`,
`is_open_access`, `has_dataset`, `dataset_urls[]`, `scraped_at`.
Index: FTS (title+abstract), year, source, scraped_at.

## 5. API

Base URL: `http://127.0.0.1:8770`

### Publik (tanpa key)
| Method | Path | Fungsi |
|---|---|---|
| GET | `/health` | status + jumlah papers |
| GET | `/docs`, `/openapi.json` | dokumentasi |

### Terproteksi (header `X-API-Key` bila auth aktif)
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/v1/datasets/search` | full-text search (`q`, `source`, `year_min`, `year_max`, `has_dataset`, `limit`, `offset`) |
| GET | `/api/v1/datasets/trend` | tren topik per tahun |
| POST | `/api/v1/citations/suggest` | saran sitasi dari teks (LLM) |
| POST | `/api/v1/similarity/check` | cek kemiripan teks vs korpus |
| POST | `/api/v1/research/title-suggest` | saran judul penelitian (LLM) |

### Contoh
```bash
curl -H "X-API-Key: <key>" \
  "http://127.0.0.1:8770/api/v1/datasets/search?q=deteksi+plagiarisme&year_min=2020"
```

### Kontrak error
- `401` — `X-API-Key` salah/kosong (saat auth aktif)
- `429` — rate limit per client terlampaui; header `Retry-After: 60`
- `503` — database tidak tersedia (`/health`)
- Respons sukses menyertakan header `X-Client: <nama-client>`.

## 6. Auth & Rate Limit (fitur shared service)

- **`CRAWLER_API_KEYS`** — `"<key>:<client>,<key2>:<client2>"`.
  - Label client opsional (default `"default"`).
  - **Kosong = auth mati** (mode dev; jangan dipakai di server shared).
- **`CRAWLER_RATE_LIMIT_PER_MINUTE`** — default 120; sliding window 60 detik,
  dihitung **per client**, in-memory (reset saat restart, cukup untuk 1 proses).
- Menambah project baru = tambahkan satu entri key baru + restart service.
  Tidak perlu perubahan kode.

## 7. Konfigurasi

Lihat `.env.example`. Ringkasan:

| Env | Default | Keterangan |
|---|---|---|
| `CRAWLER_DATABASE_URL` | `postgresql://postgres:postgres@127.0.0.1:5432/revisi_crawler` | Postgres hasil crawl |
| `CRAWLER_API_HOST` / `CRAWLER_API_PORT` | `127.0.0.1` / `8770` | bind API |
| `CRAWLER_API_KEYS` | *(kosong = auth off)* | key per client |
| `CRAWLER_RATE_LIMIT_PER_MINUTE` | `120` | per client |
| `OLLAMA_URL` / `OLLAMA_MODEL` | `http://127.0.0.1:11434` / `qwen2.5:7b-instruct` | LLM sitasi/judul |

## 8. Operasional

### Setup
```bash
cd ~/crawler-service
python3 -m venv .venv && .venv/bin/pip install -U pip -r requirements.txt
cp .env.example .env   # isi CRAWLER_API_KEYS!
```

### Menjalankan API
```bash
.venv/bin/uvicorn api.main:app --host 127.0.0.1 --port 8770
# atau: systemctl --user start crawler-api (lihat deploy/)
```

### Crawl
```bash
.venv/bin/scrapy crawl eprints \
  -a base_url=https://eprints.ums.ac.id -a query="machine learning" -a max_pages=3
# atau batch: scripts/seed-crawl.sh
```

### Catatan produksi (server hpc-ai)
- Unit aktif saat ini: `revisi-crawler.service` → masih menunjuk
  `~/studio-revisi-core/crawler`. Migrasi ke folder ini = buat venv, set
  `.env` (WAJIB isi `CRAWLER_API_KEYS`), update `WorkingDirectory`/`ExecStart`,
  `systemctl --user daemon-reload && restart`.
- Bind tetap `127.0.0.1` — konsumen di mesin yang sama. Kalau perlu lintas
  mesin, pakai reverse proxy + TLS, jangan buka 0.0.0.0 langsung.

## 9. Metrik Keberhasilan

- ≥2 project aktif memakai service dengan API key masing-masing.
- p95 `/datasets/search` < 300 ms (query FTS ter-index).
- 0 insiden "tetangga berisik": tidak ada client yang melebihi limit tanpa 429.
- Korpus bertambah lewat crawl terjadwal tanpa intervensi manual.

## 10. Risiko & Mitigasi

| Risiko | Mitigasi |
|---|---|
| Situs EPrints berubah markup | Spider berbasis Dublin Core (stabil antar versi EPrints); fallback regex |
| Crawl terlalu agresif → IP diblok kampus | `DOWNLOAD_DELAY=1.5`, 2 req/domain; jangan turunkan |
| Rate limit in-memory hilang saat restart | Diterima untuk 1 proses; kalau perlu multi-proses → Redis |
| Key bocor di repo konsumen | Key hanya di `.env`/secret store; rotasi = ganti 1 entri |
| Ollama down → citations/title-suggest gagal | Endpoint search/trend tetap hidup (tak tergantung LLM) |

## 11. Roadmap

- [ ] Spider tambahan: OJS (jurnal kampus), Garuda, Rama Repository
- [ ] Crawl terjadwal via systemd timer (per sumber, per minggu)
- [ ] Endpoint `/api/v1/stats` per client (observability pemakaian)
- [ ] Rate limit backed Redis bila pindah multi-worker
- [ ] Embedding search (pgvector) di samping FTS

## 12. Changelog Keputusan

- **2026-08-03** — Dipisah dari monorepo studio-revisi (history dipertahankan via
  `git subtree split`). Ditambah auth multi-client + rate limit per client.
  Sumber kanonik: repo ini; copy di `studio-revisi-core/crawler` akan pensiun.
- **2026-08-01** — (di studio-revisi-core) mode produksi systemd + dep `httpx`.
- **2026-07** — Bump dependency rawan (Scrapy ≥2.14.2, Twisted ≥26.4) per Dependabot.
