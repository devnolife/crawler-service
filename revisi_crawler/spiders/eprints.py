"""Generic EPrints spider.

Indonesian university repositories almost universally run EPrints (UMS, UPI,
UI library, ITB, ITS, UGM etc.). Their pages expose Dublin Core meta tags,
which makes scraping pleasant.

Run examples:

    # Search UMS for "machine learning", 3 pages, store JSON.
    scrapy crawl eprints \\
        -a base_url=https://eprints.ums.ac.id \\
        -a query="machine learning" \\
        -a max_pages=3 \\
        -O output/ums-ml.json

    # Search another EPrints repo.
    scrapy crawl eprints \\
        -a base_url=https://eprints.uny.ac.id \\
        -a query="pendidikan matematika" \\
        -a max_pages=2 \\
        -O output/uny-edu.json
"""
from __future__ import annotations

import re
from typing import Iterable
from urllib.parse import urlencode, urljoin

import scrapy

from revisi_crawler.items import ResearchItem


class EprintsSpider(scrapy.Spider):
    name = "eprints"

    # Be polite per-site. Override at crawl time via -s if needed.
    custom_settings = {
        "DOWNLOAD_DELAY": 1.5,
        "CONCURRENT_REQUESTS_PER_DOMAIN": 2,
    }

    def __init__(
        self,
        base_url: str = "https://eprints.ums.ac.id",
        query: str = "machine learning",
        max_pages: str | int = 2,
        *args,
        **kwargs,
    ) -> None:
        super().__init__(*args, **kwargs)
        self.base_url = base_url.rstrip("/")
        self.query = query
        self.max_pages = int(max_pages)
        self.allowed_domains = [self._host(self.base_url)]
        self._page_seen = 0

    # ---------------------------- helpers ----------------------------

    @staticmethod
    def _host(url: str) -> str:
        return re.sub(r"^https?://", "", url).split("/", 1)[0]

    def _build_search_url(self, offset: int = 0) -> str:
        # EPrints "simple search" form. `_offset` paginates by 20 results.
        params = {
            "q": self.query,
            "_action_search": "Search",
            "_order": "bytitle",
            "basic_srchtype": "ALL",
            "_satisfyall": "ALL",
        }
        if offset:
            params["_offset"] = offset
        return f"{self.base_url}/cgi/search/simple?{urlencode(params)}"

    # ---------------------------- crawl ----------------------------

    def start_requests(self) -> Iterable[scrapy.Request]:
        yield scrapy.Request(self._build_search_url(0), callback=self.parse_search)

    def parse_search(self, response: scrapy.http.Response):
        self._page_seen += 1
        # Each result link points at /id/eprint/NNN
        record_links = response.css(
            'p.ep_search_result a[href*="/id/eprint/"]::attr(href)'
        ).getall()
        # Fallback selector (some EPrints themes drop the wrapper class).
        if not record_links:
            record_links = response.css(
                'a[href*="/id/eprint/"]::attr(href)'
            ).re(r"https?://[^\"]+/id/eprint/\d+/?$")

        self.logger.info(
            "search page %d → %d records", self._page_seen, len(record_links)
        )

        for href in record_links:
            yield response.follow(href, callback=self.parse_record)

        # Pagination: continue while we still see results and haven't hit cap.
        if record_links and self._page_seen < self.max_pages:
            next_offset = self._page_seen * 20
            # EPrints paginates cached searches via `search_offset` links that
            # embed the server-side cache id + search expression. Several repos
            # (e.g. digilib.uin-suka.ac.id) ignore a hand-built `_offset` and
            # just re-serve page 1, so prefer following the real pagination link
            # rendered on the page. Fall back to the legacy `_offset` URL for
            # older themes that don't render one.
            next_href = None
            for href in response.css(
                'a[href*="search_offset="]::attr(href)'
            ).getall():
                m = re.search(r"search_offset=(\d+)", href)
                if m and int(m.group(1)) == next_offset:
                    next_href = href
                    break
            if next_href:
                yield response.follow(next_href, callback=self.parse_search)
            else:
                yield scrapy.Request(
                    self._build_search_url(next_offset), callback=self.parse_search
                )

    def parse_record(self, response: scrapy.http.Response):
        meta = self._collect_dublin_core(response)

        item = ResearchItem()
        item["source"] = f"eprints:{self._host(self.base_url)}"
        item["source_id"] = self._extract_eprint_id(response.url)
        item["url"] = response.url

        item["title"] = meta.get("DC.title", "").strip()
        item["authors"] = [a.strip(" ,") for a in meta.get_all("DC.creator")]
        item["year"] = self._parse_year(meta.get("DC.date"))
        item["journal"] = meta.get("DC.publisher") or meta.get("eprints.publisher") or ""
        item["keywords"] = self._split_subjects(meta.get_all("DC.subject"))

        # Abstract usually lives in <h2>Abstract</h2> + adjacent <p>.
        abstract = response.css(
            'h2:contains("Abstract") + p::text, '
            'h2:contains("ABSTRAK") + p::text, '
            'div.abstract p::text'
        ).get()
        item["abstract"] = (abstract or "").strip()

        # PDF links exposed via DC.identifier.
        pdf_urls = [
            urljoin(response.url, u)
            for u in meta.get_all("DC.identifier")
            if u.lower().endswith(".pdf")
        ]
        item["dataset_urls"] = pdf_urls
        item["has_dataset"] = bool(pdf_urls)
        item["is_open_access"] = bool(pdf_urls)  # EPrints PDFs are typically open.

        yield item

    # ---------------------------- parsing ----------------------------

    @staticmethod
    def _extract_eprint_id(url: str) -> str:
        m = re.search(r"/eprint/(\d+)", url)
        return m.group(1) if m else ""

    @staticmethod
    def _parse_year(raw: str | None) -> int | None:
        if not raw:
            return None
        m = re.search(r"(19|20)\d{2}", raw)
        return int(m.group(0)) if m else None

    @staticmethod
    def _split_subjects(raws: list[str]) -> list[str]:
        subjects: list[str] = []
        for r in raws:
            subjects.extend(s.strip() for s in r.split(";") if s.strip())
        return subjects

    def _collect_dublin_core(self, response: scrapy.http.Response) -> "_MetaBag":
        bag = _MetaBag()
        for tag in response.css("meta"):
            name = tag.attrib.get("name", "")
            content = tag.attrib.get("content", "")
            if name:
                bag.add(name, content)
        return bag


class _MetaBag:
    """Simple multimap for HTML <meta> tags so DC.creator can repeat."""

    def __init__(self) -> None:
        self._data: dict[str, list[str]] = {}

    def add(self, key: str, value: str) -> None:
        self._data.setdefault(key, []).append(value)

    def get(self, key: str, default: str | None = None) -> str | None:
        vs = self._data.get(key)
        return vs[0] if vs else default

    def get_all(self, key: str) -> list[str]:
        return list(self._data.get(key, []))
