# crawler-service — layanan crawling akademik bersama

Shared service untuk semua project (studio-revisi, wizard-research, dll):
crawl repositori EPrints kampus Indonesia → Postgres → HTTP API dengan
auth API key per client. **Detail lengkap: lihat [PRD.md](PRD.md).**

> Status: produksi (API :8770) + 1 spider generik EPrints.
> Target awal: `eprints.ums.ac.id`. Pattern berlaku untuk repository EPrints
> Indonesia lainnya (UI, ITB, ITS, UGM, UNY, UPI dll.).

## Struktur

```
crawler/
├── revisi_crawler/
│   ├── __init__.py
│   ├── settings.py
│   ├── items.py            # schema ResearchItem
│   ├── pipelines.py        # TimestampPipeline
│   └── spiders/
│       ├── __init__.py
│       └── eprints.py      # spider generic EPrints
├── output/                 # hasil crawl (gitignored)
├── requirements.txt
├── scrapy.cfg
└── README.md
```

## Setup di server (`studio-server`)

```bash
ssh studio-server
cd ~/studio-revisi/crawler
python3 -m venv .venv
source .venv/bin/activate
pip install -U pip
pip install -r requirements.txt
```

## Jalankan spider EPrints

```bash
# UMS, keyword "machine learning", 2 halaman search (max 40 record)
scrapy crawl eprints \
  -a base_url=https://eprints.ums.ac.id \
  -a query="machine learning" \
  -a max_pages=2 \
  -O output/ums-ml.json
```

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
  "scraped_at": "2026-05-21T06:30:00+00:00"
}
```

## Etika & rate-limit

- `ROBOTSTXT_OBEY = True`
- `DOWNLOAD_DELAY = 1.5` detik
- `CONCURRENT_REQUESTS_PER_DOMAIN = 2`
- `AUTOTHROTTLE_ENABLED = True` — auto-adjust based on latency
- User-Agent: `revisi-studio-crawler/0.1 (+https://revisi-studio.id)`

## Roadmap

- [ ] Spider tambahan: `data_go_id.py` (CKAN API), `bps.py`, `openalex.py`
- [ ] Output ke Postgres (bukan JSON) supaya bisa di-query webapp
- [ ] APScheduler / systemd timer: re-crawl mingguan
- [ ] REST endpoint di FastAPI: `GET /api/v1/datasets/search?q=...`
- [ ] Webapp page: `/research/data-finder` consume endpoint di atas

## Catatan deploy

`garuda.kemdikbud.go.id` ngga resolve dari DNS internal `studio-server`
(systemd-resolved nolak `.kemdikbud.go.id`). Pakai EPrints atau alternatif
lain dulu sampai DNS dibetulin.
